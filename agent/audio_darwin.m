// System audio capture with ScreenCaptureKit (macOS 13+), for audio_darwin.go.
// Uses the Screen Recording grant the agent already has. SCK delivers 48 kHz
// float32; it is handed to Go as interleaved stereo.
#import <Foundation/Foundation.h>
#import <CoreMedia/CoreMedia.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

API_AVAILABLE(macos(13.0))
@interface RDAudioTap : NSObject <SCStreamOutput, SCStreamDelegate>
@property(strong) SCStream *stream;
@end

@implementation RDAudioTap
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
	if (type != SCStreamOutputTypeAudio || !CMSampleBufferIsValid(sb)) return;
	CMFormatDescriptionRef fd = CMSampleBufferGetFormatDescription(sb);
	const AudioStreamBasicDescription *asbd = fd ? CMAudioFormatDescriptionGetStreamBasicDescription(fd) : NULL;
	if (!asbd || asbd->mFormatID != kAudioFormatLinearPCM || !(asbd->mFormatFlags & kAudioFormatFlagIsFloat) || asbd->mBitsPerChannel != 32) return;
	int frames = (int)CMSampleBufferGetNumSamples(sb);
	int ch = (int)asbd->mChannelsPerFrame;
	if (frames <= 0 || ch <= 0) return;
	size_t need = 0;
	if (CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(sb, &need, NULL, 0, NULL, NULL, 0, NULL) != noErr || need == 0) return;
	AudioBufferList *abl = malloc(need);
	CMBlockBufferRef bb = NULL;
	if (CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(sb, NULL, abl, need, NULL, NULL,
			kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, &bb) == noErr) {
		int planar = (asbd->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
		float *out = malloc((size_t)frames * 2 * sizeof(float));
		for (int c = 0; c < 2; c++) {
			int src = c < ch ? c : 0; // mono → both channels
			if (planar && (UInt32)src >= abl->mNumberBuffers) src = 0;
			const float *p = planar ? abl->mBuffers[src].mData : abl->mBuffers[0].mData;
			size_t avail = (planar ? abl->mBuffers[src].mDataByteSize : abl->mBuffers[0].mDataByteSize) / sizeof(float);
			for (int f = 0; f < frames; f++) {
				size_t i = planar ? (size_t)f : (size_t)f * ch + src;
				out[f * 2 + c] = i < avail ? p[i] : 0;
			}
		}
		rdAudioPCM(out, frames);
		free(out);
		CFRelease(bb);
	}
	free(abl);
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
	rdAudioStopped((char *)(error.localizedDescription.UTF8String ?: "stopped"));
}
@end

static id tap; // RDAudioTap; one capture at a time (audio_darwin.go serializes)

int rd_audio_available(void) {
	if (@available(macOS 13.0, *)) return 1;
	return 0;
}

static char *errstr(NSError *e, const char *fallback) {
	return strdup(e ? e.localizedDescription.UTF8String : fallback);
}

// rd_audio_start returns NULL, or an error message to free().
char *rd_audio_start(void) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			__block SCShareableContent *content = nil;
			__block NSError *err = nil;
			dispatch_semaphore_t sem = dispatch_semaphore_create(0);
			[SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES
				completionHandler:^(SCShareableContent *c, NSError *e) { content = c; err = e; dispatch_semaphore_signal(sem); }];
			if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) != 0) return strdup("ScreenCaptureKit: timeout");
			if (!content || content.displays.count == 0) return errstr(err, "ScreenCaptureKit: no display");

			SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:content.displays[0] excludingWindows:@[]];
			SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
			cfg.capturesAudio = YES;
			cfg.excludesCurrentProcessAudio = YES;
			cfg.sampleRate = 48000;
			cfg.channelCount = 2;
			// The video side is unavoidable; keep it as cheap as possible.
			cfg.width = 16;
			cfg.height = 16;
			cfg.minimumFrameInterval = CMTimeMake(1, 1);
			cfg.showsCursor = NO;

			RDAudioTap *t = [[RDAudioTap alloc] init];
			SCStream *s = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:t];
			dispatch_queue_t q = dispatch_queue_create("vn.tuandv.remotedesk.audio", DISPATCH_QUEUE_SERIAL);
			NSError *e2 = nil;
			if (![s addStreamOutput:t type:SCStreamOutputTypeAudio sampleHandlerQueue:q error:&e2]) return errstr(e2, "addStreamOutput failed");
			// Without a screen output SCK logs an error for every (tiny) video frame.
			[s addStreamOutput:t type:SCStreamOutputTypeScreen sampleHandlerQueue:q error:nil];

			err = nil;
			[s startCaptureWithCompletionHandler:^(NSError *e) { err = e; dispatch_semaphore_signal(sem); }];
			if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) != 0) return strdup("ScreenCaptureKit: start timeout");
			if (err) return errstr(err, "start failed");
			t.stream = s;
			tap = t;
			return NULL;
		}
	}
	return strdup("cần macOS 13 trở lên");
}

void rd_audio_stop(void) {
	if (@available(macOS 13.0, *)) {
		RDAudioTap *t = tap;
		tap = nil;
		if (!t || !t.stream) return;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		[t.stream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(sem); }];
		dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
		t.stream = nil;
	}
}
