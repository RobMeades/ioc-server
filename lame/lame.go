package lame

/*
#cgo LDFLAGS: -L. -lmp3lame
#include "lame/lame.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

type Handle *C.struct_lame_global_struct

const (
	STEREO        = C.STEREO
	JOINT_STEREO  = C.JOINT_STEREO
	DUAL_CHANNEL  = C.DUAL_CHANNEL /* LAME doesn't supports this! */
	MONO          = C.MONO
	NOT_SET       = C.NOT_SET
	MAX_INDICATOR = C.MAX_INDICATOR
	BIT_DEPTH     = 16
)

const (
    VBR_OFF            = C.vbr_off
    VBR_MT             = C.vbr_mt
    VBR_RH             = C.vbr_rh
    VBR_ABR            = C.vbr_abr
    VBR_MTRH           = C.vbr_mtrh
    VBR_MAX_INDICATOR  = C.vbr_max_indicator
    VBR_DEFAULT        = C.vbr_default
)

type Encoder struct {
	handle    Handle
	remainder []byte
	closed    bool
}

func Init() *Encoder {
	handle := C.lame_init()
	encoder := &Encoder{handle, make([]byte, 0), false}
	runtime.SetFinalizer(encoder, finalize)
	return encoder
}

func (e *Encoder) SetNumChannels(num int) {
	C.lame_set_num_channels(e.handle, C.int(num))
}

func (e *Encoder) SetInSamplerate(sampleRate int) {
	C.lame_set_in_samplerate(e.handle, C.int(sampleRate))
}

func (e *Encoder) SetBitrate(bitRate int) {
	C.lame_set_brate(e.handle, C.int(bitRate))
}

func (e *Encoder) GetBitrate() int {
	retcode:= C.lame_get_brate(e.handle)
	return int(retcode)
}

func (e *Encoder) SetMode(mode C.MPEG_mode) {
	C.lame_set_mode(e.handle, mode)
}

func (e *Encoder) SetVBR(mode C.vbr_mode) {
	C.lame_set_VBR(e.handle, mode)
}

func (e *Encoder) SetVBRQuality(quality float32) {
	C.lame_set_VBR_quality(e.handle, C.float(quality))
}

func (e *Encoder) SetQuality(quality int) {
	 C.lame_set_quality(e.handle, C.int(quality))
}

func (e *Encoder) SetGenre(genre string) {
	C.id3tag_set_genre(e.handle, C.CString(genre))
}

func (e *Encoder) InitParams() int {
	retcode := C.lame_init_params(e.handle)
	return int(retcode)
}

func (e *Encoder) GetPadding() int {
    retcode := C.lame_get_encoder_padding(e.handle)
    return int(retcode)
}

func (e *Encoder) GetEncoderDelay() int {
    retcode := C.lame_get_encoder_delay(e.handle)
    return int(retcode)    
}

func (e *Encoder) GetMp3FrameSize() int {
    retcode := C.lame_get_framesize(e.handle)
    return int(retcode)
}

func (e *Encoder) GetSizeMp3NotWritten() int {
    retcode := C.lame_get_size_mp3buffer(e.handle)
    return int(retcode)    
}

func (e *Encoder) GetSizePcmUnencoded() int {
    retcode := C.lame_get_mf_samples_to_encode(e.handle)
    return int(retcode)    
}

func (e *Encoder) DisableReservoir() {
    C.lame_set_disable_reservoir(e.handle, 1)
}

func (e *Encoder) EnableReservoir() {
    C.lame_set_disable_reservoir(e.handle, 0)
}

func (e *Encoder) NumChannels() int {
	n := C.lame_get_num_channels(e.handle)
	return int(n)
}

func (e *Encoder) Bitrate() int {
	br := C.lame_get_brate(e.handle)
	return int(br)
}

func (e *Encoder) Mode() int {
	m := C.lame_get_mode(e.handle)
	return int(m)
}

func (e *Encoder) Quality() int {
	q := C.lame_get_quality(e.handle)
	return int(q)
}

// Default = 0 = lame chooses.  -1 = disabled 
func (e *Encoder) LowPassFrequency(frequency int) {
	C.lame_set_lowpassfreq(e.handle, C.int(frequency))
}

func (e *Encoder) InSamplerate() int {
	sr := C.lame_get_in_samplerate(e.handle)
	return int(sr)
}

func (e *Encoder) Encode(buf []byte) []byte {

	if len(e.remainder) > 0 {
		buf = append(e.remainder, buf...)
	}

	if len(buf) == 0 {
		return make([]byte, 0)
	}

	blockAlign := BIT_DEPTH / 8 * e.NumChannels()

	remainBytes := len(buf) % blockAlign
	if remainBytes > 0 {
		e.remainder = buf[len(buf)-remainBytes : len(buf)]
		buf = buf[0 : len(buf)-remainBytes]
	} else {
		e.remainder = make([]byte, 0)
	}

	numSamples := len(buf) / blockAlign
	estimatedSize := int(1.25*float64(numSamples) + 7200)
	out := make([]byte, estimatedSize)

	cBuf := (*C.short)(unsafe.Pointer(&buf[0]))
	cOut := (*C.uchar)(unsafe.Pointer(&out[0]))

	bytesOut := C.int(C.lame_encode_buffer(
		e.handle,
		cBuf,
		nil,
		C.int(numSamples),
		cOut,
		C.int(estimatedSize),
	))
	return out[0:bytesOut]

}

func (e *Encoder) Flush() []byte {
	estimatedSize := 7200
	out := make([]byte, estimatedSize)
	cOut := (*C.uchar)(unsafe.Pointer(&out[0]))
	bytesOut := C.int(C.lame_encode_flush(
		e.handle,
		cOut,
		C.int(estimatedSize),
	))

	return out[0:bytesOut]
}

func (e *Encoder) Close() {
	if e.closed {
		return
	}
	C.lame_close(e.handle)
	e.closed = true
}

func finalize(e *Encoder) {
	e.Close()
}
