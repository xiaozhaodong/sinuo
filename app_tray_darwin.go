//go:build darwin && !server

package main

/*
#cgo CFLAGS: -mmacosx-version-min=12.0 -x objective-c
#cgo LDFLAGS: -mmacosx-version-min=12.0 -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdatomic.h>

static atomic_bool trayPowerOff = false;
static id trayPowerObserver = nil;

// 由 Wails 主线程调用。显示窗口时恢复 Regular，并再次激活、置前。
static void traySetWindowVisible(void *ptr, int visible) {
    [NSApp setActivationPolicy:visible ? NSApplicationActivationPolicyRegular : NSApplicationActivationPolicyAccessory];
    if (visible) {
        NSWindow *window = (NSWindow *)ptr;
        [window deminiaturize:nil];
        [window makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
        // ActivationPolicy 切换会异步更新 Dock，下一轮再确保焦点落到主窗口。
        dispatch_async(dispatch_get_main_queue(), ^{
            if (window.visible) {
                [window makeKeyAndOrderFront:nil];
                [NSApp activateIgnoringOtherApps:YES];
            }
        });
    }
}

static void trayObservePowerOff(void) {
    if (trayPowerObserver != nil) return;
    trayPowerObserver = [[[NSWorkspace sharedWorkspace] notificationCenter]
        addObserverForName:NSWorkspaceWillPowerOffNotification object:nil queue:nil
        usingBlock:^(NSNotification *note) { atomic_store(&trayPowerOff, true); }];
}

static int trayIsPowerOff(void) { return atomic_load(&trayPowerOff); }

static void trayActivateForDialog(void) {
    [NSApp activateIgnoringOtherApps:YES];
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

func installTrayPlatformHooks() { C.trayObservePowerOff() }
func traySystemEnding() bool    { return C.trayIsPowerOff() != 0 }
func trayActivateForDialog()    { C.trayActivateForDialog() }

func setTrayWindowVisible(window *application.WebviewWindow, visible bool) {
	value := C.int(0)
	if visible {
		value = 1
	}
	C.traySetWindowVisible(window.NativeWindow(), value)
}
