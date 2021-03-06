/* Audio input (UDP or TCP server) for the Internet of Chuffs.
 *
 * Copyright (C) u-blox Melbourn Ltd
 * u-blox Melbourn Ltd, Melbourn, UK
 *
 * All rights reserved.
 *
 * This source file is the sole property of u-blox Melbourn Ltd.
 * Reproduction or utilization of this source in whole or part is
 * forbidden without the written consent of u-blox Melbourn Ltd.
 */

package main

import (
    "fmt"
    "net"
    "os"
    "log"
    "bytes"
    "time"
//    "encoding/hex"
)

//--------------------------------------------------------------------
// Types
//--------------------------------------------------------------------

// Struct to hold a URTP datagram
type UrtpDatagram struct {
    SequenceNumber  uint16
    Timestamp       uint64
    Audio           *[]int16
}

// Where we are in reassembling a URTP packet (required for TCP reception)
type TcpReassemblyData struct {
    State         int
    ByteCount     int
    PayloadSize   int
    Header        bytes.Buffer
    Datagram      bytes.Buffer
}

//--------------------------------------------------------------------
// Constants
//--------------------------------------------------------------------

// The duration of a block of incoming audio in ms
const BLOCK_DURATION_MS int = 20

// The sampling frequency of the incoming audio
const SAMPLING_FREQUENCY int = 16000

// The number of samples per block
const SAMPLES_PER_BLOCK int = SAMPLING_FREQUENCY * BLOCK_DURATION_MS / 1000

// UNICAM parameters
const SAMPLES_PER_UNICAM_BLOCK int = SAMPLING_FREQUENCY / 1000
const UNICAM_CODED_SHIFT_SIZE_BITS int = 4

// The URTP datagram parameters
const SYNC_BYTE byte = 0x5a
const URTP_TIMESTAMP_SIZE int = 8
const URTP_SEQUENCE_NUMBER_SIZE int = 2
const URTP_PAYLOAD_SIZE_SIZE int = 2
const URTP_HEADER_SIZE int = 14
const URTP_SAMPLE_SIZE int = 2
const URTP_DATAGRAM_MAX_SIZE int = URTP_HEADER_SIZE + SAMPLES_PER_BLOCK * URTP_SAMPLE_SIZE

// Frequency at which to return timing datagrams
const TIMING_DATAGRAM_PERIOD time.Duration = 1000 * time.Millisecond

// Offset to the number of bytes part of the URTP header
const URTP_NUM_BYTES_AUDIO_OFFSET int = 12

// The overhead to add to the URTP datagram size to give a good IP buffer size for
// one packet
const IP_HEADER_OVERHEAD int = 40

// The audio coding schemes
const (
    PCM_SIGNED_16_BIT = 0
    UNICAM_COMPRESSED_8_BIT = 1
    MAX_NUM_AUDIO_CODING_SCHEMES = iota
)

// URTP reassembly states (needed for TCP reception)
const (
    URTP_STATE_WAITING_SYNC = iota
    URTP_STATE_WAITING_AUDIO_CODING = iota
    URTP_STATE_WAITING_SEQUENCE_NUMBER = iota
    URTP_STATE_WAITING_TIMESTAMP = iota
    URTP_STATE_WAITING_PAYLOAD_SIZE = iota
    URTP_STATE_WAITING_PAYLOAD = iota
)

//--------------------------------------------------------------------
// Variables
//--------------------------------------------------------------------

// A buffer for TCP data
var tcpBuffer bytes.Buffer

// The last time a timing datagram was sent
var timingDatagramSent time.Time

// Deemphasis filter required for unicam
var deemphasis Fir

// Notch filter to remove squeal from the Hologram Nova modem
var desqueal DeSquealFir

//--------------------------------------------------------------------
// Functions
//--------------------------------------------------------------------

// Decode PCM_SIGNED_16_BIT data from a datagram
// For details of the format, see the client code (ioc-client)
func decodePcm(audioDataPcm []byte) *[]int16 {
    audio := make([]int16, len(audioDataPcm) / URTP_SAMPLE_SIZE)

    // Just copy in the bytes
    x := 0
    for y := range audio {
        audio[y] = (int16(audioDataPcm[x]) << 8) + int16(audioDataPcm[x + 1])
        x += 2
    }

    return &audio
}

// Decode UNICAM_COMPRESSED_8_BIT_16000_HZ data from a datagram
// For details of the format, see the client code (ioc-client)
func decodeUnicam(audioDataUnicam []byte, sampleSizeBits int) *[]int16 {
    var numBlocks int
    var blockOffset int
    var blockCount int
    var shiftValues byte
    var shift byte
    var peakShift byte
    var sample int16
    var sourceIndex int

    // Work out how much audio data is present
    for x := 0; x < len(audioDataUnicam) * 8; x += SAMPLES_PER_UNICAM_BLOCK * sampleSizeBits + UNICAM_CODED_SHIFT_SIZE_BITS {
        numBlocks++;
    }

    // Allocate space
    audio := make([]int16, numBlocks * SAMPLES_PER_UNICAM_BLOCK)

    //log.Printf("UNICAM: %d byte(s) containing %d block(s), expanding to a total of %d samples(s) of uncompressed audio.\n", len(audioDataUnicam), numBlocks, len(audio))

    // Decode the blocks
    for blockCount < numBlocks {
        // Get the compressed values
        for x := 0; x < SAMPLES_PER_UNICAM_BLOCK; x++ {
            audio[blockOffset + x] = int16(audioDataUnicam[sourceIndex])
            sourceIndex++
        }

        // Get the shift value
        if ((blockCount & 1) == 0) {
            // Even block
            shiftValues = audioDataUnicam[sourceIndex]
            sourceIndex++
            shift = shiftValues & 0x0F
        } else {
            shift = shiftValues >> 4
        }

        if shift > peakShift {
            peakShift = shift
        }

        //log.Printf("UNICAM block %d, shift value %d.\n", blockCount, shift)
        // Shift the values to uncompress them
        for x := 0; x < SAMPLES_PER_UNICAM_BLOCK; x++ {
            // Check if the top bit is set and, if so, sign extend
            sample = audio[blockOffset + x]
            if sample & (1 << (uint(sampleSizeBits) - 1)) != 0 {
                for y := uint(sampleSizeBits); y < uint(URTP_SAMPLE_SIZE) * 8; y++ {
                    sample |= (1 << y)
                }
            }
            
            // Put the sample through the filters on the way into
            // the audio slice
            FirPut(&deemphasis, float32(sample << shift))
            DeSquealFirPut(&desqueal, FirGet(&deemphasis))
            audio[blockOffset + x] = int16(DeSquealFirGet(&desqueal))

            //log.Printf("UNICAM block %d:%02d, compressed value %d (0x%x) becomes %d (0x%x).\n",
            //           blockCount, x, sample, sample, audio[blockOffset + x], audio[blockOffset + x])
        }

        blockOffset += SAMPLES_PER_UNICAM_BLOCK
        blockCount++
    }
    //log.Printf("UNICAM highest shift value was %d.\n", peakShift)
    return &audio
}

// Handle an incoming URTP datagram and send it off for processing
// For details of the format, see the client code (ioc-client).
// This function returns a timing datagram which may be sent back
// to the source if required
func handleUrtpDatagram(packet []byte) []byte {
    var timingDatagram []byte
    //log.Printf("Packet of size %d byte(s) received.\n", len(packet))
    //log.Printf("%s\n", hex.Dump(line[:numBytesIn]))
    if (len(packet) >= URTP_HEADER_SIZE) {
        // Populate a URTP datagram with the data
        urtpDatagram := new(UrtpDatagram)
        //log.Printf("URTP header:\n")
        //log.Printf("  sync byte:        0x%x.\n", packet[0])
        audioCodingScheme := packet[1]
        urtpDatagram.SequenceNumber = uint16(packet[2]) << 8 + uint16(packet[3])
        //log.Printf("  sequence number:  %d.\n", urtpDatagram.SequenceNumber)
        urtpDatagram.Timestamp = (uint64(packet[4]) << 56) + (uint64(packet[5]) << 48) + (uint64(packet[6]) << 40) + (uint64(packet[7]) << 32) +
                                 (uint64(packet[8]) << 24) + (uint64(packet[9]) << 16) + (uint64(packet[10]) << 8) + uint64(packet[11])
        //log.Printf("  timestamp:        %6.3f ms.\n", float64(urtpDatagram.Timestamp) / 1000)

        if (len(packet) > URTP_HEADER_SIZE) {
            switch (audioCodingScheme) {
                case PCM_SIGNED_16_BIT:
                    //log.Printf("  audio coding:     PCM_SIGNED_16_BIT.\n")
                    urtpDatagram.Audio = decodePcm(packet[URTP_HEADER_SIZE:])
                case UNICAM_COMPRESSED_8_BIT:
                    //log.Printf("  audio coding:     UNICAM_COMPRESSED_8_BIT.\n")
                    urtpDatagram.Audio = decodeUnicam(packet[URTP_HEADER_SIZE:], 8)
                default:
                    //log.Printf("  audio coding:     !unknown!\n")
            }
        }

        if urtpDatagram.Audio != nil {
            //log.Printf("URTP sample(s) %d\n", len(*urtpDatagram.Audio))
        } else {
            //log.Printf("Unable to decode audio samples from this datagram.\n")
        }

        // Create the timing datagram
        timingDatagram = append(timingDatagram, packet[0], packet[2], packet[3], packet[4], packet[5], packet[6], packet[7], packet[8], packet[9], packet[10], packet[11])

        // Send the data to the processing channel
        ProcessDatagramsChannel <- urtpDatagram
    }

    return timingDatagram
}

// Verify that a sequence of byte represents URTP header
// For details of the format, see the client code (ioc-client)
func verifyUrtpHeader(header []byte) bool {
    var isHeader bool

    if len(header) >= URTP_HEADER_SIZE {
        if header[0] == SYNC_BYTE {
            if header[1] < MAX_NUM_AUDIO_CODING_SCHEMES {
                bytesOfPayload := ((int(header[URTP_NUM_BYTES_AUDIO_OFFSET]) << 8) + (int(header[URTP_NUM_BYTES_AUDIO_OFFSET + 1])))
                if bytesOfPayload <= URTP_DATAGRAM_MAX_SIZE {
                    isHeader = true;
                } else {
                    log.Printf("NOT a URTP header %x (%d (0x%x, in the last two bytes) is larger than the maximum number of payload bytes (%d)).\n", header,
                               bytesOfPayload, bytesOfPayload, URTP_DATAGRAM_MAX_SIZE)
                }
            } else {
                log.Printf("NOT a URTP header %x (0x%x in the second byte is not a valid audio coding scheme).\n", header, header[1])
            }
        } else {
            log.Printf("NOT a URTP header %x (0x%x at the start is not a sync byte (%x)).\n", header, header[0], SYNC_BYTE)
        }
    } else {
        log.Printf("NOT a URTP header %x (must be at least %d bytes long).\n", header, URTP_HEADER_SIZE)
    }

    return isHeader
}

// Handle a stream of (e.g. TCP) bytes containing URTP datagrams
// For details of the format, see the client code (ioc-client)
// A timing datagram may be returned if the stream has reached a point
// where one can be created
func handleUrtpStream(reassemblyData *TcpReassemblyData, data []byte) []byte {
    var err error
    var item byte
    var timingDatagram []byte

    // Write all the data to the TCP buffer
    tcpBuffer.Write(data)

    //log.Printf("TCP reassembly: %d byte(s) received.\n", len(data))
    for item, err = tcpBuffer.ReadByte(); err == nil; item, err = tcpBuffer.ReadByte() {
        //log.Printf("TCP reassembly: state %d, byte %d (0x%x).\n", reassemblyData.State, item, item)
        switch (reassemblyData.State) {
            case URTP_STATE_WAITING_SYNC:
                // Look for the sync byte
                if item == SYNC_BYTE {
                    reassemblyData.Header.WriteByte(item)
                    reassemblyData.State = URTP_STATE_WAITING_AUDIO_CODING
                } else {                
                    //log.Printf("TCP reassembly: awaiting initial sync byte but 0x%x isn't one (0x%x).\n", item, SYNC_BYTE)
                    reassemblyData.Header.Reset()
                    reassemblyData.State = URTP_STATE_WAITING_SYNC
                }
            case URTP_STATE_WAITING_AUDIO_CODING:
                // Look for the audio coding scheme and check it
                if item < MAX_NUM_AUDIO_CODING_SCHEMES {
                    reassemblyData.Header.WriteByte(item)
                    //log.Printf("TCP reassembly: audio coding scheme 0x%x.\n", item)
                    reassemblyData.State = URTP_STATE_WAITING_SEQUENCE_NUMBER
                } else {
                    log.Printf("TCP reassembly: audio coding scheme in the second byte (0x%0x) is not a valid audio coding scheme.\n", item)
                    reassemblyData.Header.Reset()
                    reassemblyData.State = URTP_STATE_WAITING_SYNC
                }
            case URTP_STATE_WAITING_SEQUENCE_NUMBER:
                // Read in the two-byte sequence number
                reassemblyData.Header.WriteByte(item)
                reassemblyData.ByteCount++
                //log.Printf("TCP reassembly: sequence number byte %d is 0x%x.\n", reassemblyData.ByteCount, item)
                if reassemblyData.ByteCount >= URTP_SEQUENCE_NUMBER_SIZE {
                    reassemblyData.ByteCount = 0
                    reassemblyData.State = URTP_STATE_WAITING_TIMESTAMP
                }
            case URTP_STATE_WAITING_TIMESTAMP:
                // Read in the eight-byte timestamp
                reassemblyData.Header.WriteByte(item)
                reassemblyData.ByteCount++
                //log.Printf("TCP reassembly: timestamp byte %d is 0x%x.\n", reassemblyData.ByteCount, item)
                if reassemblyData.ByteCount >= URTP_TIMESTAMP_SIZE {
                    reassemblyData.ByteCount = 0
                    reassemblyData.State = URTP_STATE_WAITING_PAYLOAD_SIZE
                }
            case URTP_STATE_WAITING_PAYLOAD_SIZE:
                // Read in the two-byte payload size
                reassemblyData.Header.WriteByte(item)
                reassemblyData.PayloadSize += int (uint(item) << uint((8 * (URTP_PAYLOAD_SIZE_SIZE - reassemblyData.ByteCount - 1))))
                reassemblyData.ByteCount++
                if reassemblyData.ByteCount >= URTP_PAYLOAD_SIZE_SIZE {
                    // Got the payload size, check it and, if it is OK, write the header
                    reassemblyData.ByteCount = 0
                    //log.Printf("TCP reassembly: URTP payload is %d byte(s).\n", reassemblyData.PayloadSize)
                    if reassemblyData.PayloadSize <= URTP_DATAGRAM_MAX_SIZE {
                        reassemblyData.State = URTP_STATE_WAITING_PAYLOAD
                        reassemblyData.Datagram.Write(reassemblyData.Header.Bytes())
                        if reassemblyData.PayloadSize == 0 {
                            reassemblyData.Header.Reset()
                            reassemblyData.State = URTP_STATE_WAITING_SYNC
                        }
                    } else {
                        //log.Printf("TCP reassembly: NOT a URTP header, payload length %d (0x%x, in the last two bytes) is larger than the maximum number of payload bytes (%d)).\n",
                        //           reassemblyData.PayloadSize, reassemblyData.PayloadSize, URTP_DATAGRAM_MAX_SIZE)
                        reassemblyData.PayloadSize = 0
                        reassemblyData.Header.Reset()
                        reassemblyData.State = URTP_STATE_WAITING_SYNC
                    }
                }
            case URTP_STATE_WAITING_PAYLOAD:
                // Write the one byte we have
                reassemblyData.Datagram.WriteByte(item)
                if reassemblyData.PayloadSize > 0 {
                    reassemblyData.PayloadSize--
                }
                // Read in as much of the rest of the payload as possible
                bytesToRead := tcpBuffer.Len()
                if bytesToRead > reassemblyData.PayloadSize {
                    bytesToRead = reassemblyData.PayloadSize
                }
                reassemblyData.Datagram.Write(tcpBuffer.Next(bytesToRead))
                reassemblyData.PayloadSize -= bytesToRead
                if reassemblyData.PayloadSize == 0 {
                    // Got the lot, handle the complete datagram now and reset the state machine
                    //log.Printf("TCP reassembly: URTP packet (%d bytes) fully received.\n", urtpDatagram.Len())
                    timingDatagram = handleUrtpDatagram(reassemblyData.Datagram.Next(reassemblyData.Datagram.Len()))
                    reassemblyData.Header.Reset()
                    reassemblyData.State = URTP_STATE_WAITING_SYNC
                } else {
                    //log.Printf("TCP reassembly: %d byte(s) of payload remaining to be read.\n", reassemblyData.PayloadSize)
                }
            default:
                reassemblyData.ByteCount = 0
                reassemblyData.PayloadSize = 0
                reassemblyData.Header.Reset()
                reassemblyData.State = URTP_STATE_WAITING_SYNC
        }
    }
    
    return timingDatagram
}

// Run a UDP server forever
func udpServer(port string) {
    var numBytesIn int
    var server *net.UDPConn
    var remoteAddress *net.UDPAddr
    line := make([]byte, URTP_DATAGRAM_MAX_SIZE)

    // Set up the server
    localUdpAddr, err := net.ResolveUDPAddr("udp", ":" + port)
    if err == nil {
        // Begin listening
        server, err = net.ListenUDP("udp", localUdpAddr)
        if err == nil {
            defer server.Close()
            fmt.Printf("UDP server listening for Chuffs on port %s.\n", port)
            err1 := server.SetReadBuffer(URTP_DATAGRAM_MAX_SIZE + IP_HEADER_OVERHEAD)
            if err1 != nil {
                log.Printf("Unable to set optimal read buffer size (%s).\n", err1.Error())
            }
            // Read UDP packets forever
            for numBytesIn, remoteAddress, err = server.ReadFromUDP(line); (err == nil) && (numBytesIn > 0); numBytesIn, remoteAddress, err = server.ReadFromUDP(line) {
                // For UDP, a single URTP datagram arrives in a single UDP packet
                if (numBytesIn >= URTP_HEADER_SIZE) && (verifyUrtpHeader(line[:URTP_HEADER_SIZE])) {
                    timingDatagram := handleUrtpDatagram(line[:numBytesIn])
                    if (len(timingDatagram) > 0) && time.Now().After(timingDatagramSent.Add(TIMING_DATAGRAM_PERIOD)) {
                        _, err = server.WriteToUDP(timingDatagram, remoteAddress)
                        if err == nil {
                            timingDatagramSent = time.Now()
                            log.Printf("Timing datagram sent to %s.\n", remoteAddress.String())
                        } else {
                            log.Printf("Couldn't send timing datagram (%s).\n", err.Error())
                        }
                    }
                }
            }
            if err != nil {
                fmt.Fprintf(os.Stderr, "Error reading from port %v (%s).\n", localUdpAddr, err.Error())
            } else {
                fmt.Fprintf(os.Stderr, "UDP read on port %v returned when it should not.\n", localUdpAddr)
            }
        } else {
            fmt.Fprintf(os.Stderr, "Couldn't start UDP server on port %s (%s).\n", port, err.Error())
        }
    } else {
        fmt.Fprintf(os.Stderr, "'%s' is not a valid UDP address (%s).\n", port, err.Error())
    }
}

// Run a TCP server forever
func tcpServer(port string) {
    var newServer net.Conn
    var currentServer net.Conn

    listener, err := net.Listen("tcp", ":" + port)
    if err == nil {
        defer listener.Close()
        // Listen for a connection
        for {
            fmt.Printf("TCP server waiting for a [further] Chuff connection on port %s.\n", port)
            newServer, err = listener.Accept()
            if err == nil {
                if currentServer != nil {
                    currentServer.Close()
                }
                currentServer = newServer
                x, success := currentServer.(*net.TCPConn)
                if success {
                    err1 := x.SetReadBuffer(30000)
                    if err1 != nil {
                        log.Printf("Unable to set optimal read buffer size (%s).\n", err1.Error())
                    }
                    err1 = x.SetNoDelay(true)
                    if err1 != nil {
                        log.Printf("Unable to switch of Nagle algorithm (%s).\n", err1.Error())
                    }
                } else {
                    log.Printf("Can't cast *net.Conn to *net.TCPConn in order to set optimal read buffer size.\n")
                }
                // Process datagrams received on the channel in another go routine
                fmt.Printf("Connection made by %s.\n", currentServer.RemoteAddr().String())
                go func(server net.Conn) {
                    var reassemblyData TcpReassemblyData
                    reassemblyData.State = URTP_STATE_WAITING_SYNC
                    // Read packets until the connection is closed under us
                    line := make([]byte, URTP_DATAGRAM_MAX_SIZE)
                    for numBytesIn, err := server.Read(line); (err == nil) && (numBytesIn > 0); numBytesIn, err = server.Read(line) {
                        timingDatagram := handleUrtpStream(&reassemblyData, line[:numBytesIn])
                        if (len(timingDatagram) > 0) && time.Now().After(timingDatagramSent.Add(TIMING_DATAGRAM_PERIOD)) {
                            numBytesOut, err := server.Write(timingDatagram)
                            if err == nil {
                                timingDatagramSent = time.Now()
                                log.Printf("Timing datagram sent, length %d byte(s).\n", numBytesOut)
                            } else {
                                log.Printf("Couldn't send timing datagram (%s).\n", err.Error())
                            }
                        }
                    }
                    fmt.Printf("[Connection to %s closed].\n", server.RemoteAddr().String())
                }(currentServer)
            } else {
                fmt.Fprintf(os.Stderr, "Error accepting connection (%s).\n", err.Error())
            }
        }
    } else {
        fmt.Fprintf(os.Stderr, "Unable to listen for TCP connections on port %s (%s).\n", port, err.Error())
    }
}

// Run the server that receives the audio of Chuffs; this function should never return
func operateAudioIn(port string) {
    // Initialise the filters
    FirInit(&deemphasis)
    DeSquealFirInit(&desqueal)
    
    go udpServer(port)
    tcpServer(port)
}
