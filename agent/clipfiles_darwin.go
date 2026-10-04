package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#include <stdlib.h>

static long rd_pb_change(void) { return [[NSPasteboard generalPasteboard] changeCount]; }

// Newline-separated paths of the file URLs on the pasteboard (malloc'd), or NULL.
static char *rd_pb_files(void) {
	@autoreleasepool {
		NSArray *urls = [[NSPasteboard generalPasteboard] readObjectsForClasses:@[[NSURL class]]
			options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
		if (urls.count == 0) return NULL;
		NSMutableString *s = [NSMutableString string];
		for (NSURL *u in urls) { [s appendString:u.path]; [s appendString:@"\n"]; }
		return strdup(s.UTF8String);
	}
}

static int rd_pb_set_files(const char *nl) {
	@autoreleasepool {
		NSMutableArray *urls = [NSMutableArray array];
		for (NSString *p in [[NSString stringWithUTF8String:nl] componentsSeparatedByString:@"\n"])
			if (p.length) [urls addObject:[NSURL fileURLWithPath:p]];
		if (urls.count == 0) return 0;
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		[pb clearContents];
		return [pb writeObjects:urls] ? 1 : 0;
	}
}
*/
import "C"

import (
	"errors"
	"strings"
	"sync"
	"unsafe"
)

var (
	pbMu     sync.Mutex
	pbChange C.long = -1
	pbFiles  []string
)

// clipboardReadFiles returns the files copied in Finder, if any. The
// pasteboard is only read when its change count moved.
func clipboardReadFiles() ([]string, error) {
	pbMu.Lock()
	defer pbMu.Unlock()
	if c := C.rd_pb_change(); c != pbChange {
		pbChange = c
		pbFiles = nil
		if s := C.rd_pb_files(); s != nil {
			pbFiles = strings.Split(strings.TrimRight(C.GoString(s), "\n"), "\n")
			C.free(unsafe.Pointer(s))
		}
	}
	return pbFiles, nil
}

// clipboardWriteFiles puts files on the pasteboard, so ⌘V in Finder copies them.
func clipboardWriteFiles(paths []string) error {
	cs := C.CString(strings.Join(paths, "\n"))
	defer C.free(unsafe.Pointer(cs))
	if C.rd_pb_set_files(cs) == 0 {
		return errors.New("không ghi được clipboard")
	}
	return nil
}
