/* Datagram processing function for the Internet of Chuffs server.
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
    "log"
    "time"
    "os"
    "path/filepath"
    "io/ioutil"
    "container/list"
    "bytes"
    "encoding/binary"
    "errors"
    "sync"
    "github.com/RobMeades/ioc-server/lame"
//    "encoding/hex"
)

//--------------------------------------------------------------------
// Types
//--------------------------------------------------------------------

// Structure to represent the state of the audio output buffer
// that we are feeding at MediaControlChannel.
type OutputBufferState struct {
    Buffered     time.Duration
    BufferSize   time.Duration
}

//--------------------------------------------------------------------
// Constants
//--------------------------------------------------------------------

// How big the processedDatagramsList can become
const NUM_PROCESSED_DATAGRAMS int = 1

// Guard against silly sequence number gaps
const MAX_GAP_FILL_MILLISECONDS int = 500

// The minimum size that we allow the buffered audio
// in MediaControlChannel to get to
const MIN_OUTPUT_BUFFERED_AUDIO time.Duration = time.Millisecond * 1000

// The track title to use
const MP3_TITLE string = "Internet of Chuffs"

// The length of the binary timestamp in the ID3 tag of the MP3 file
const MP3_ID3_TAG_TIMESTAMP_LEN int = 8

//--------------------------------------------------------------------
// Variables
//--------------------------------------------------------------------

// The channel that processes incoming datagrams
var ProcessDatagramsChannel chan<- interface{}

// An audio buffer to hold raw PCM samples received from the client
var pcmAudio bytes.Buffer

// Prefix that represents the fixed portion of a "PRIV" ID3 tag to put at the start of a
// segment file, see https://tools.ietf.org/html/draft-pantos-http-live-streaming-23#section-3.4
// and http://id3.org/id3v2.3.0#ID3v2_overview
//
// The generic portion of the prefix consists of:
//   - a 10-byte ID3 header, containing:
//     - the characters "ID3",
//     - two bytes of ID3 version number, set to 0x0400,
//     - one byte of ID3 flags, set to 0,
//     - four bytes of ID3 tag size where the most significant bit (bit 7) is set to
//       zero in every byte, making a total of 28 bits; the zeroed bits are ignored, so
//       a 257 bytes long tag is represented as 0x00 0x00 0x02 0x01; in our case
//       the size is 0x3f (63).
//   - an ID3 body, containing:
//     - four characters of frame ID, in our case "PRIV",
//     - four bytes of size, calculated as the whole ID frame size minus the 10-byte ID3 header
//       so in our case 0x35 (53),
//     - two bytes of flags, set to 0.
// The "PRIV" ID3 tag, which is used in our case, consists of:
//   - an owner identifier string followed by 0x00, in our case "com.apple.streaming.transportStreamTimestamp\x00",
//   - MP3_ID3_TAG_TIMESTAMP_LEN octets of big-endian binary timestamp on a 90 kHz basis.
//
// Only the fixed portion of the PRIV ID3 tag is included in this variable, the MP3_ID3_TAG_TIMESTAMP_LEN bytes of timestamp must be
// written separately.
var id3Prefix string = "ID3\x04\x00\x00\x00\x00\x00\x3fPRIV\x00\x00\x00\x35\x00\x00com.apple.streaming.transportStreamTimestamp\x00"

//--------------------------------------------------------------------
// Functions
//--------------------------------------------------------------------

// Open an MP3 file
func openMp3File(dirName string) *os.File {
    handle, err := ioutil.TempFile (dirName, "")
    if err == nil {
        filePath := handle.Name()
        handle.Close()
        if os.Rename(filePath, filePath + SEGMENT_EXTENSION) == nil {
            handle, err = os.Create(filePath + SEGMENT_EXTENSION)
            log.Printf("Opened segment file \"%s\" for MP3 output.\n", handle.Name())
        } else {
            log.Printf("Unable to rename temporary file \"%s\" to \"%s\".\n", filePath, filePath + SEGMENT_EXTENSION)
        }
    } else {
        log.Printf("Unable to create segment file for MP3 output in directory \"%s\".\n", dirName)
    }

    return handle
}

// Create an MP3 writer
func createMp3Writer(mp3Audio *bytes.Buffer) (*lame.LameWriter, int) {
    var mp3SamplesPerFrame int
    // Initialise the MP3 encoder.  This is equivalent to:
    // lame -V2 -r -s 16000 -m m --bitwidth 16 <input file> <output file>
    mp3Writer := lame.NewWriter(mp3Audio)
    if mp3Writer != nil {
        mp3Writer.Encoder.SetInSamplerate(SAMPLING_FREQUENCY)
        mp3Writer.Encoder.SetNumChannels(1)
        mp3Writer.Encoder.SetMode(lame.MONO)
        // VBR writes tags into the file which makes
        // hls.js think the file isn't an MP3 file (as
        // the first MP3 header must appear within the
        // first 100 bytes of the file).  So don't do that.
        mp3Writer.Encoder.SetVBR(lame.VBR_OFF)
        // The encode keeps 4 bits free in case of rapid gain
        // changes; some of that loss can be recovered here
        mp3Writer.Encoder.SetScale(7)
        // Disabling the bit reservoir reduces quality
        // but allows consecutive MP3 files to be butted
        // up together without any gaps
        mp3Writer.Encoder.DisableReservoir()
        mp3Writer.Encoder.SetGenre("144") // Thrash metal
        // Note: bit depth defaults to 16
        if mp3Writer.Encoder.InitParams() >= 0 {
            mp3SamplesPerFrame = mp3Writer.Encoder.GetMp3FrameSize()
            log.Printf("Created MP3 writer, MP3 frame size is %d samples, encoder delay is %d samples.\n",
                       mp3SamplesPerFrame, mp3Writer.Encoder.GetEncoderDelay())
        } else {
            mp3Writer.Close()
            mp3Writer = nil
            log.Printf("Unable to initialise MP3 writer.\n")
        }
    } else {
        log.Printf("Unable to instantiate MP3 writer.\n")
    }

    return mp3Writer, mp3SamplesPerFrame
}

// Handle a gap of a given number of samples in the input data
func handleGap(gap int, previousDatagram * UrtpDatagram) {
    var y int

    log.Printf("Handling a gap of %d samples...\n", gap)
    if gap < SAMPLING_FREQUENCY * MAX_GAP_FILL_MILLISECONDS / 1000 {
        // TODO: for now just repeat the last sample we received
        fill := make([]byte, gap * URTP_SAMPLE_SIZE)
        if (previousDatagram != nil) && (len(*previousDatagram.Audio) > 0) {
            for w := 0; w < len(fill); w += URTP_SAMPLE_SIZE {
                x := (*previousDatagram.Audio)[y]
                for z := 0; z < URTP_SAMPLE_SIZE; z++ {
                    fill[w + z] = byte(x >> ((uint(z) * 8)))
                }
                y++
                if y >= len(*previousDatagram.Audio) {
                    y = 0
                }
            }
        }
        log.Printf("Writing %d bytes to the audio buffer...\n", len(fill))
        pcmAudio.Write(fill)
    } else {
        log.Printf("Ignored a silly gap.\n")
    }
}

// Process a URTP datagram
func processDatagram(datagram * UrtpDatagram, savedDatagramList * list.List) {
    var previousDatagram *UrtpDatagram

    if savedDatagramList.Front() != nil {
        previousDatagram = savedDatagramList.Front().Value.(*UrtpDatagram)
    }

    //log.Printf("Processing a datagram...\n")

    // Handle the case where we have missed some datagrams
    if (previousDatagram != nil) && (datagram.SequenceNumber != previousDatagram.SequenceNumber + 1) {
        log.Printf("Sequence number skip (expected %d, received %d).\n", previousDatagram.SequenceNumber + 1, datagram.SequenceNumber)
        handleGap(int(datagram.SequenceNumber - previousDatagram.SequenceNumber) * SAMPLES_PER_BLOCK, previousDatagram)
    }

    // Copy the received audio into the buffer
    if datagram.Audio != nil {
        audioBytes := make([]byte, len(*datagram.Audio) * URTP_SAMPLE_SIZE)
        for x, y := range *datagram.Audio {
            for z := 0; z < URTP_SAMPLE_SIZE; z++ {
                audioBytes[(x * URTP_SAMPLE_SIZE) + z] = byte(y >> ((uint(z) * 8)))
            }
        }
        //log.Printf("Writing %d bytes to the audio buffer...\n", len(audioBytes))
        pcmAudio.Write(audioBytes)

        // If the block is shorter than expected, handle that gap too
        if len(*datagram.Audio) < SAMPLES_PER_BLOCK {
            handleGap(SAMPLES_PER_BLOCK - len(*datagram.Audio), previousDatagram)
        }
    } else {
        // And if the audio is entirely missing, handle that
        handleGap(SAMPLES_PER_BLOCK, previousDatagram)
    }
}

// Encode up to numSamples into the output stream
func encodeOutput (mp3Writer *lame.LameWriter, pcmHandle *os.File, numSamples int) int {
    var err error
    var bytesRead int
    var bytesEncoded int
    buffer := make([]byte, numSamples * URTP_SAMPLE_SIZE)

    bytesRead, err = pcmAudio.Read(buffer)
    if bytesRead > 0 {
        //log.Printf("Encoding %d byte(s) into the output...\n", bytesRead)
        if mp3Writer != nil {
            bytesEncoded, err = mp3Writer.Write(buffer[:bytesRead])
            if err != nil {
                log.Printf("Unable to encode MP3.\n")
            }
        }
        if pcmHandle != nil {
            _, err = pcmHandle.Write(buffer[:bytesRead])
            if err != nil {
                log.Printf("Unable to write to PCM file.\n")
            }
        }
    }

    return bytesEncoded / URTP_SAMPLE_SIZE
}

// Write the ID3 tag to the start of an MP3 segment file indicating
// its time offset from the previous segment file
func writeTag(mp3Handle *os.File, offset time.Duration) error {
    var timestampBytes bytes.Buffer
    var timestampUint64 uint64 // Must be an uint64 to produce the correct sized timestamp

    // First, write the prefix
    _, err := mp3Handle.WriteString(id3Prefix)
    if err == nil {
        // Then write the binary timestamp offset on a 90 kHz basis
        timestampUint64 = uint64(float32(offset) / float32(time.Microsecond) * float32(90000) / float32(1000000))
        err := binary.Write(&timestampBytes, binary.BigEndian, timestampUint64)
        if err == nil {
            if timestampBytes.Len() != MP3_ID3_TAG_TIMESTAMP_LEN {
                err = errors.New(fmt.Sprintf("Timestamp is of incorrect size (%d byte(s) (0x%x) when size must be %d byte(s)).\n", timestampBytes.Len(), &timestampBytes, MP3_ID3_TAG_TIMESTAMP_LEN))
            }
        } else {
            log.Printf("Error creating timestamp offset (%s).\n", err.Error())
        }

        log.Printf("Writing %d byte timestamp inside MP3 file (0x%x)...\n", timestampBytes.Len(), &timestampBytes)
        _, err = timestampBytes.WriteTo(mp3Handle)
    }

    return err
}

// Do the processing; this function should never return
func operateAudioProcessing(pcmHandle *os.File, mp3Dir string, maxOosTimeSeconds uint, segmentFileDurationMilliseconds uint) {
    var newDatagramList = list.New()
    var newDatagramListLocker sync.Mutex
    var processedDatagramList = list.New()
    var mp3Audio bytes.Buffer
    var mp3Writer *lame.LameWriter
    var mp3SamplesPerFrame int
    var mp3Handle *os.File
    var mp3Duration time.Duration
    var mp3FileSamples int = int(segmentFileDurationMilliseconds) * SAMPLING_FREQUENCY / 1000
    var maxOosAge time.Duration = time.Second * time.Duration(maxOosTimeSeconds)
    var oosAge time.Duration
    var mp3SamplesToEncode int
    var samplesEncoded int
    var mp3Offset time.Duration
    var minOutputBufferedAudio time.Duration = MIN_OUTPUT_BUFFERED_AUDIO
    var channel = make(chan interface{})
    processTicker := time.NewTicker(time.Duration(BLOCK_DURATION_MS) * time.Millisecond)

    ProcessDatagramsChannel = channel

    // Initialise the linked list of datagrams
    newDatagramList.Init()

    // Create the MP3 writer
    mp3Writer, mp3SamplesPerFrame = createMp3Writer(&mp3Audio)
    if mp3Writer == nil {
        fmt.Fprintf(os.Stderr, "Unable to create MP3 writer.\n")
        os.Exit(-1)
    }
    // Encode an exact number of MP3 frames
    mp3SamplesToEncode = mp3FileSamples / mp3SamplesPerFrame *  mp3SamplesPerFrame

    // Create the first MP3 output file
    mp3Handle = openMp3File(mp3Dir)
    if mp3Handle == nil {
        fmt.Fprintf(os.Stderr, "Unable to create temporary file for MP3 output in directory \"%s\" (permissions?).\n", mp3Dir)
        os.Exit(-1)
    }

    fmt.Printf("Audio processing channel created and now being serviced.\n")

    // Timed function that processes received datagrams and feeds the output stream
    go func() {
        for _ = range processTicker.C {
            var next *list.Element
            // Go through the list of newly arrived datagrams, processing them and moving
            // them to the processed list
            newDatagramListLocker.Lock()
            thingProcessed := false
            for newElement := newDatagramList.Front(); newElement != nil; newElement = next {
                next = newElement.Next(); // Get the next value for the following iteration
                                          // as a Remove() would cause newElement.next()
                                          // to return nil
                processDatagram(newElement.Value.(*UrtpDatagram), processedDatagramList)
                //log.Printf("%d byte(s) in the outgoing audio buffer.\n", pcmAudio.Len())
                //log.Printf("Moving datagram from the new list to the processed list...\n")
                processedDatagramList.PushFront(newElement.Value)
                thingProcessed = true
                newDatagramList.Remove(newElement)
            }
            newDatagramListLocker.Unlock()
            if thingProcessed {
                oosAge = time.Duration(0)
                count := 0
                for processedElement := processedDatagramList.Front(); processedElement != nil; processedElement = next {
                    next = processedElement.Next(); // Get the next value for the following iteration
                                              // as a Remove() would cause newElement.next()
                                              // to return nil
                    count++
                    if count > NUM_PROCESSED_DATAGRAMS {
                        //log.Printf("Removing a datagram from the processed list...\n")
                        processedDatagramList.Remove(processedElement)
                        //log.Printf("%d datagram(s) now in the processed list.\n", processedDatagramList.Len())
                    }
                }
            } else {
                // If nothing has been processed, add to the out of service age and,
                // if it gets too large, reset the stream
                oosAge += time.Duration(BLOCK_DURATION_MS) * time.Millisecond
                if (oosAge > maxOosAge) {
                    oosAge = time.Duration(0)
                    mp3Offset = time.Duration(0)
                    samplesEncoded = 0;
                    mp3SamplesToEncode = mp3FileSamples / mp3SamplesPerFrame *  mp3SamplesPerFrame
                    reset := new(Reset)
                    MediaControlChannel <- reset
                }
            }

            // Always have to encode something into the output stream
            samples := encodeOutput(mp3Writer, pcmHandle, mp3SamplesToEncode)
            samplesEncoded += samples
            mp3SamplesToEncode -= samples

            if mp3SamplesToEncode <= 0 {
                if mp3Handle != nil {
                    mp3Duration = time.Duration(samplesEncoded * 1000000 / SAMPLING_FREQUENCY) * time.Microsecond
                    log.Printf("Writing %d millisecond(s) of MP3 audio (%d samples) to \"%s\" at offset %6.3f (PCM buffer is %6.3f s, MP3 buffer is %d byte(s), URTP list is %d deep).\n",
                               mp3Duration / time.Millisecond, samplesEncoded, mp3Handle.Name(), float64(mp3Offset) / float64(time.Second),
                               float64(pcmAudio.Len() / URTP_SAMPLE_SIZE * 1000) / float64(SAMPLING_FREQUENCY) / float64(1000), mp3Audio.Len(), newDatagramList.Len())
                    err := writeTag(mp3Handle, mp3Offset)
                    if err == nil {
                        _, err = mp3Audio.WriteTo(mp3Handle)
                        mp3Handle.Close()
                        //log.Printf("Closed MP3 file.\n")
                        if err == nil {
                            // Let the audio output channel know of the new audio file
                            mp3AudioFile := new(Mp3AudioFile)
                            mp3AudioFile.fileName = filepath.Base(mp3Handle.Name())
                            mp3AudioFile.title = MP3_TITLE
                            mp3AudioFile.timestamp = time.Now()
                            mp3AudioFile.duration = mp3Duration
                            mp3AudioFile.usable = true;
                            mp3AudioFile.removable = false;
                            MediaControlChannel <- mp3AudioFile
                        } else {
                            log.Printf("There was an error writing to \"%s\" (%s).\n", mp3Handle.Name(), err.Error())
                        }
                    } else {
                        mp3Handle.Close()
                        log.Printf("There was an error writing the ID3 tag to \"%s\", closing MP3 file (%s).\n", mp3Handle.Name(), err.Error())
                    }
                }
                mp3Offset += mp3Duration
                mp3Handle = openMp3File(mp3Dir)
                samplesEncoded = 0
                mp3SamplesToEncode = mp3FileSamples / mp3SamplesPerFrame *  mp3SamplesPerFrame
            }
        }
    }()

    // Process datagrams received on the channel
    go func() {
        for cmd := range channel {
            switch message := cmd.(type) {
                // Handle datagrams, throw everything else away
                case *UrtpDatagram:
                {
                    //log.Printf("Adding a new datagram to the FIFO list...\n")
                    newDatagramListLocker.Lock()
                    newDatagramList.PushBack(message)
                    newDatagramListLocker.Unlock()
                }
                // If the output buffer has got too low then send a silence frame
                // of one MP3 file duration
                case *OutputBufferState:
                {
                    log.Printf("Output buffer has %d ms of buffered audio.\n", message.Buffered / time.Millisecond)
                    if (minOutputBufferedAudio > message.BufferSize / 2) {
                        minOutputBufferedAudio = message.BufferSize / 2
                    }
                    if (message.Buffered < MIN_OUTPUT_BUFFERED_AUDIO) && (mp3Handle != nil) {
                        // Add a sample of silence if it has got too low so that HLS doesn't run dry (which would stop
                        // the browser requesting refills)
                        buffer := make([]byte, (mp3FileSamples / mp3SamplesPerFrame *  mp3SamplesPerFrame) * URTP_SAMPLE_SIZE)
                        log.Printf("Adding %d samples (%d milliseconds) of silence into the PCM stream.\n",
                                    len(buffer) / URTP_SAMPLE_SIZE, (len(buffer) / URTP_SAMPLE_SIZE) * 1000 / SAMPLING_FREQUENCY)
                        pcmAudio.Write(buffer)
                    }
                }
            }
        }
        fmt.Printf("Audio processing channel closed, stopping.\n")
    }()
}

/* End Of File */
