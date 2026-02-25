---
name: imsg
description: Send iMessage/SMS via Messages.app using AppleScript (osascript).
homepage: https://imsg.to
triggers:
  - type: keyword
    pattern: message
  - type: command
    pattern: /send
metadata:
  {
    "openclaw":
      {
        "emoji": "📨",
        "os": ["darwin"],
        "requires": { "bins": ["osascript"] },
      },
  }
---

# imsg Actions

## Overview

Use `osascript` to send Messages.app iMessage/SMS on macOS via AppleScript.

Requirements: Messages.app signed in, Full Disk Access for your terminal, and Automation permission to control Messages.app.

## Inputs to collect

- Recipient handle (phone number or email) of the buddy in Messages.
- Message text to send.

## Actions

### Send a message

```bash
osascript -e 'tell application "Messages" to send "你好，这是测试消息" to buddy "phone number"'
```

## Notes

- Replace the phone number and message text with the desired recipient and content.
- Confirm recipient and message before sending.
