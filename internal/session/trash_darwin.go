package session

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>

// Moves path to the Trash. Returns NULL on success, or a malloc'd description of the
// failure that the caller frees.
static char *recordo_trash(const char *path) {
	@autoreleasepool {
		NSString *p = [NSString stringWithUTF8String:path];
		if (p == nil) {
			return strdup("path is not valid UTF-8");
		}
		NSError *error = nil;
		if ([[NSFileManager defaultManager] trashItemAtURL:[NSURL fileURLWithPath:p]
		                                  resultingItemURL:nil
		                                             error:&error]) {
			return NULL;
		}
		const char *msg = error.localizedDescription.UTF8String;
		return strdup(msg != NULL ? msg : "unknown error");
	}
}
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

// Trash moves the whole recording to the Trash, so a mistaken delete stays recoverable.
func (s *Session) Trash() error {
	// Lossy, as the Rust version's to_string_lossy: Foundation wants UTF-8.
	path := C.CString(strings.ToValidUTF8(s.Dir, "�"))
	defer C.free(unsafe.Pointer(path))
	if msg := C.recordo_trash(path); msg != nil {
		defer C.free(unsafe.Pointer(msg))
		return errors.New("could not move to Trash: " + C.GoString(msg))
	}
	return nil
}
