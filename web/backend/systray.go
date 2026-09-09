//go:build !android && ((!darwin && !freebsd) || cgo)

package main

import (
	"fmt"

	"fyne.io/systray"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/web/backend/utils"
)

func runTray() {
	systray.Run(onReady, onExit)
}

// trayGatewayAutoStart reads gateway.auto_start from the handler's config
// file (nil = unset = default true).
func trayGatewayAutoStart() bool {
	if apiHandler == nil {
		return true
	}
	return apiHandler.GatewayAutoStartEnabled()
}

// trayLaunchAtLoginEnabled reports whether the launcher is registered to
// start at OS login (same source as the settings page).
func trayLaunchAtLoginEnabled() bool {
	if apiHandler == nil {
		return false
	}
	enabled, _, _, err := apiHandler.GetAutoStartStatus()
	if err != nil {
		return false
	}
	return enabled
}

// traySetGatewayAutoStart persists gateway.auto_start through the handler's
// patch pipeline (same load → merge → security restore → validate → save flow
// as PATCH /api/config). The watchdog re-reads the config on every probe, so
// the change pauses/resumes restarts within one interval; the watchdog loop
// itself stays alive either way. Returns whether the toggle was persisted.
func traySetGatewayAutoStart(enabled bool) bool {
	if apiHandler == nil {
		logger.Errorf("Cannot save gateway.auto_start: handler unavailable")
		return false
	}
	if err := apiHandler.PatchConfigFile(map[string]any{
		"gateway": map[string]any{"auto_start": enabled},
	}); err != nil {
		logger.Errorf("Failed to save gateway.auto_start: %v", err)
		return false
	}
	logger.Infof("gateway.auto_start set to %v via tray", enabled)
	return true
}

// onReady is called when the system tray is ready
func onReady() {
	// Set icon and tooltip
	systray.SetIcon(getIcon())
	systray.SetTooltip(fmt.Sprintf(T(AppTooltip), appName))

	// Create menu items
	mOpen := systray.AddMenuItem(T(MenuOpen), T(MenuOpenTooltip))
	mAbout := systray.AddMenuItem(T(MenuAbout), T(MenuAboutTooltip))

	// Add version info under About menu
	mVersion := mAbout.AddSubMenuItem(fmt.Sprintf(T(MenuVersion), appVersion), T(MenuVersionTooltip))
	mVersion.Disable()
	mRepo := mAbout.AddSubMenuItem(T(MenuGitHub), "")
	mDocs := mAbout.AddSubMenuItem(T(MenuDocs), "")

	systray.AddSeparator()

	// Auto-start toggles (persisted immediately, no restart needed).
	// "Auto-start Gateway" = gateway.auto_start watchdog (config file);
	// "Launch at Login" = launcher autostart at OS login (startup.go APIs).
	mAutoStart := systray.AddMenuItemCheckbox(T(MenuAutoStart), T(MenuAutoStartHint), trayGatewayAutoStart())
	mLaunchAtLogin := systray.AddMenuItemCheckbox(T(MenuLaunchAtLogin), T(MenuLaunchAtLoginH), trayLaunchAtLoginEnabled())

	systray.AddSeparator()

	// Add restart option
	mRestart := systray.AddMenuItem(T(MenuRestart), T(MenuRestartTooltip))

	systray.AddSeparator()

	// Quit option
	mQuit := systray.AddMenuItem(T(MenuQuit), T(MenuQuitTooltip))

	// Handle menu clicks
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if err := openBrowser(); err != nil {
					logger.Errorf("Failed to open browser: %v", err)
				}

			case <-mVersion.ClickedCh:
				// Version info - do nothing, just shows current version

			case <-mRepo.ClickedCh:
				if err := utils.OpenBrowser("https://github.com/sipeed/picoclaw"); err != nil {
					logger.Errorf("Failed to open GitHub: %v", err)
				}

			case <-mDocs.ClickedCh:
				if err := utils.OpenBrowser(T(DocUrl)); err != nil {
					logger.Errorf("Failed to open docs: %v", err)
				}

			case <-mAutoStart.ClickedCh:
				// Flip the checkbox only when the new value was persisted, so
				// the visual state never diverges from the config file.
				enabled := trayGatewayAutoStart()
				if traySetGatewayAutoStart(!enabled) {
					if !enabled {
						mAutoStart.Check()
					} else {
						mAutoStart.Uncheck()
					}
				}

			case <-mLaunchAtLogin.ClickedCh:
				if apiHandler != nil {
					enabled, _, _, err := apiHandler.GetAutoStartStatus()
					if err != nil {
						logger.Errorf("Failed to read launch-at-login status: %v", err)
						continue
					}
					if err := apiHandler.SetAutoStart(!enabled); err != nil {
						logger.Errorf("Failed to update launch-at-login setting: %v", err)
						continue
					}
					if !enabled {
						mLaunchAtLogin.Check()
					} else {
						mLaunchAtLogin.Uncheck()
					}
				}

			case <-mRestart.ClickedCh:
				fmt.Println("Restart request received...")
				if apiHandler != nil {
					if pid, err := apiHandler.RestartGateway(); err != nil {
						logger.Errorf("Failed to restart gateway: %v", err)
					} else {
						logger.Infof("Gateway restarted (PID: %d)", pid)
					}
				}

			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	if !*noBrowser {
		// Auto-open browser after systray is ready (if not disabled)
		// Check no-browser flag via environment or pass as parameter if needed
		if err := openBrowser(); err != nil {
			logger.Errorf("Warning: Failed to auto-open browser: %v", err)
		}
	}
}

// onExit is called when the system tray is exiting
func onExit() {
	logger.Info(T(Exiting))
}
