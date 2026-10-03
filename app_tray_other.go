//go:build !darwin || server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func installTrayPlatformHooks()                                 {}
func traySystemEnding() bool                                    { return false }
func trayActivateForDialog()                                    {}
func setTrayWindowVisible(_ *application.WebviewWindow, _ bool) {}
