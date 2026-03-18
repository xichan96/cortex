---
name: macos-alert
description: Send system alerts/notifications with custom sound effects on macOS using osascript.
homepage: https://github.com/example/macos-alert
triggers:
  - type: keyword
    pattern: alert
  - type: keyword
    pattern: notify
  - type: command
    pattern: /alert
  - type: command
    pattern: /notify
metadata:
  {
    "openclaw":
      {
        "emoji": "🔔",
        "os": ["darwin"],
        "requires": { "bins": ["osascript"] },
      },
  }
---

# macos-alert Actions

## Overview

Use `osascript` to send system notifications/alerts with custom sound effects on macOS (Notification Center or modal dialog).
Supports all built-in macOS sound effects (SMS, Ping, Pop, Tink, Glass, etc.).

Requirements: Notification permissions enabled for the terminal/app executing the command (System Settings > Notifications > Terminal).

## Inputs to collect

- Alert type (notification = non-blocking / dialog = blocking modal).
- Alert title (main header text).
- Alert message (body content, optional subtitle for notifications).
- Sound effect name (use built-in macOS sound names like SMS, Ping, Pop).

## Actions

### 1. Send non-blocking Notification Center alert (with sound)
Basic notification with title, message and custom sound:
```bash
osascript -e 'display notification "{MESSAGE_TEXT}" with title "{ALERT_TITLE}" sound name "{SOUND_NAME}"'