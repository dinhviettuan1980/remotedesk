// Display names for display_darwin.go (NSScreen has the user-visible ones).
#import <AppKit/AppKit.h>
#include <string.h>

int rd_display_name(unsigned int did, char *buf, int len) {
	@autoreleasepool {
		for (NSScreen *s in [NSScreen screens]) {
			NSNumber *n = s.deviceDescription[@"NSScreenNumber"];
			if (n && n.unsignedIntValue == did) {
				if (@available(macOS 10.15, *)) {
					const char *name = s.localizedName.UTF8String;
					if (name && *name) {
						strlcpy(buf, name, len);
						return 1;
					}
				}
			}
		}
	}
	return 0;
}
