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

static int trayReduceMotion(void) {
    return [[NSWorkspace sharedWorkspace] accessibilityDisplayShouldReduceMotion] ? 1 : 0;
}

static id trayMenuObserver = nil;

static void trayPrepareMenuImages(NSMenu *menu) {
    for (NSMenuItem *item in menu.itemArray) {
        if (item.image != nil) {
            if (!item.image.isTemplate) item.image.template = YES;
            // macOS 27 起 AppKit 默认隐藏菜单项图片（连 SF Symbol 也不画），须显式设为 Visible（= 1）。
            // 用 KVC 写入，旧 SDK 也能编译；旧系统没有这个属性，图片本就显示。
            if ([item respondsToSelector:@selector(setPreferredImageVisibility:)]) {
                [item setValue:@1 forKey:@"preferredImageVisibility"];
            }
        }
        if (item.hasSubmenu) trayPrepareMenuImages(item.submenu);
    }
}

// Wails 设置菜单项图片时既不标模板，也不设可见性；托盘菜单开始跟踪、显示之前补上，
// 图标才会显示，并由系统按深色模式、高亮行与置灰着色（真机验证此时机早于建行）。
// Wails 打开托盘菜单前会把 delegate 设为 StatusItemController，以此只处理托盘菜单，
// 不碰 WebView 右键菜单里「共享」等彩色应用图标。
static void trayObserveMenuTracking(void) {
    if (trayMenuObserver != nil) return;
    Class trayDelegate = NSClassFromString(@"StatusItemController");
    if (trayDelegate == nil) return;
    trayMenuObserver = [[NSNotificationCenter defaultCenter]
        addObserverForName:NSMenuDidBeginTrackingNotification object:nil queue:nil
        usingBlock:^(NSNotification *note) {
            NSMenu *menu = note.object;
            if ([menu.delegate isKindOfClass:trayDelegate]) trayPrepareMenuImages(menu);
        }];
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

const trayMenuIcons = true

func installTrayPlatformHooks() {
	C.trayObservePowerOff()
	C.trayObserveMenuTracking()
}

func traySystemEnding() bool { return C.trayIsPowerOff() != 0 }
func trayActivateForDialog() { C.trayActivateForDialog() }
func trayReduceMotion() bool { return C.trayReduceMotion() != 0 }

func setTrayWindowVisible(window *application.WebviewWindow, visible bool) {
	value := C.int(0)
	if visible {
		value = 1
	}
	C.traySetWindowVisible(window.NativeWindow(), value)
}
