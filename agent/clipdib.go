package main

// Windows clipboard bitmaps (CF_DIB) ⇄ PNG; pure Go so it is tested everywhere.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
)

var errClipboardNotAnImage = errors.New("not an image")

// dibToPNG converts a packed DIB (BITMAPINFOHEADER + pixels), 24 or 32 bpp.
func dibToPNG(dib []byte) ([]byte, error) {
	if len(dib) < 40 {
		return nil, errClipboardNotAnImage
	}
	le := binary.LittleEndian
	hdr := int(le.Uint32(dib[0:]))
	w := int(int32(le.Uint32(dib[4:])))
	h := int(int32(le.Uint32(dib[8:])))
	bpp := int(le.Uint16(dib[14:]))
	comp := le.Uint32(dib[16:])
	if hdr < 40 || w <= 0 || h == 0 || (bpp != 24 && bpp != 32) || (comp != 0 && comp != 3) {
		return nil, errors.New("unsupported clipboard bitmap")
	}
	off := hdr
	if comp == 3 && hdr == 40 { // BI_BITFIELDS: three masks follow (assume BGR order)
		off += 12
	}
	topDown := h < 0
	if topDown {
		h = -h
	}
	stride := (w*bpp + 31) / 32 * 4
	if off+stride*h > len(dib) {
		return nil, errors.New("truncated clipboard bitmap")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	if bpp == 32 {
		for i := off + 3; i < off+stride*h; i += 4 {
			if dib[i] != 0 {
				anyAlpha = true
				break
			}
		}
	}
	for y := 0; y < h; y++ {
		row := y
		if !topDown {
			row = h - 1 - y
		}
		src := dib[off+row*stride:]
		for x := 0; x < w; x++ {
			px := src[x*bpp/8:]
			a := uint8(255)
			if bpp == 32 && anyAlpha {
				a = px[3]
			}
			img.SetNRGBA(x, y, color.NRGBA{R: px[2], G: px[1], B: px[0], A: a})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// imageToDIB: 32 bpp BI_RGB, bottom-up, alpha kept in the fourth byte.
func imageToDIB(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 40+w*h*4)
	le := binary.LittleEndian
	le.PutUint32(out[0:], 40)
	le.PutUint32(out[4:], uint32(w))
	le.PutUint32(out[8:], uint32(h))
	le.PutUint16(out[12:], 1)
	le.PutUint16(out[14:], 32)
	le.PutUint32(out[20:], uint32(w*h*4))
	for y := 0; y < h; y++ {
		row := out[40+(h-1-y)*w*4:]
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.B, c.G, c.R, c.A
		}
	}
	return out
}
