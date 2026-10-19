// Clipboard images for clipimg_darwin.go (NSPasteboard: PNG, or TIFF converted).
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

long rd_clip_seq(void) {
	return (long)[NSPasteboard generalPasteboard].changeCount;
}

// rd_clip_image: 1 when the pasteboard holds an image and no text.
int rd_clip_image_only(void) {
	@autoreleasepool {
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		NSArray *types = pb.types;
		if ([types containsObject:NSPasteboardTypeString] || [types containsObject:NSPasteboardTypeFileURL]) return 0;
		return [types containsObject:NSPasteboardTypePNG] || [types containsObject:NSPasteboardTypeTIFF];
	}
}

// rd_clip_read_png returns malloc'ed PNG bytes (or NULL).
void *rd_clip_read_png(long *len) {
	@autoreleasepool {
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		NSData *png = [pb dataForType:NSPasteboardTypePNG];
		if (!png) {
			NSData *tiff = [pb dataForType:NSPasteboardTypeTIFF];
			if (!tiff) return NULL;
			NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:tiff];
			png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
			if (!png) return NULL;
		}
		void *buf = malloc(png.length);
		memcpy(buf, png.bytes, png.length);
		*len = (long)png.length;
		return buf;
	}
}

int rd_clip_write_png(const void *data, long len) {
	@autoreleasepool {
		NSData *png = [NSData dataWithBytes:data length:len];
		NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:png];
		if (!rep) return 0;
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		[pb clearContents];
		// TIFF too: some apps only read that.
		[pb declareTypes:@[NSPasteboardTypePNG, NSPasteboardTypeTIFF] owner:nil];
		[pb setData:png forType:NSPasteboardTypePNG];
		NSData *tiff = rep.TIFFRepresentation;
		if (tiff) [pb setData:tiff forType:NSPasteboardTypeTIFF];
		return 1;
	}
}
