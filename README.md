# telehaptic

Listens for incoming Telegram messages from unmuted chats and triggers a haptic feedback device via HTTP.

## Setup

Create a `.env` file:

```
TG_API_ID=12345678
TG_API_HASH=abcdef1234567890abcdef1234567890
TG_PASSWORD=your_2fa_password
```

`TG_API_ID` and `TG_API_HASH` are obtained from https://my.telegram.org.
`TG_PASSWORD` is only required if your account has two-factor authentication enabled.

## Run

```
go run .
```

On first run, a QR code will open in the browser. Scan it via Telegram: Settings -> Devices -> Link Desktop Device. The session is saved to `session.json` and reused on subsequent runs.

The list of unmuted peers is cached in `unmuted.json`. To force a refresh:

```
go run . --refresh
```
