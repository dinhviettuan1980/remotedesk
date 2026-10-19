package main

/*
#include <stdlib.h>
long rd_clip_seq(void);
int rd_clip_image_only(void);
void *rd_clip_read_png(long *len);
int rd_clip_write_png(const void *data, long len);
*/
import "C"

import (
	"errors"
	"unsafe"
)

// clipboardSeq changes whenever anything is copied.
func clipboardSeq() uint64 { return uint64(C.rd_clip_seq()) }

// clipboardImage returns the clipboard image as PNG when it holds an image
// and no text (copying text in some apps adds a picture of it too).
func clipboardImage() ([]byte, error) {
	if C.rd_clip_image_only() != 1 {
		return nil, nil
	}
	var n C.long
	p := C.rd_clip_read_png(&n)
	if p == nil {
		return nil, errors.New("could not read the clipboard image")
	}
	defer C.free(p)
	return C.GoBytes(p, C.int(n)), nil
}

func clipboardWriteImage(png []byte) error {
	if len(png) == 0 {
		return errors.New("empty image")
	}
	if C.rd_clip_write_png(unsafe.Pointer(&png[0]), C.long(len(png))) != 1 {
		return errors.New("not an image")
	}
	return nil
}
