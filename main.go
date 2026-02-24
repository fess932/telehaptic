package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
	"rsc.io/qr"
)

const (
	hapticURL = "https://local.jmw.nz:41443/haptic/mad"
	cacheFile = "unmuted.json"
)

var httpClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("env " + key + " is not set")
	}
	return v
}

func triggerHaptic() {
	httpClient.Post(hapticURL, "", strings.NewReader(""))
}

func showQR(token qrlogin.Token) {
	code, err := qr.Encode(token.URL(), qr.H)
	if err != nil {
		fmt.Println(token.URL())
		return
	}

	size := code.Size
	const pad = 4
	total := size + pad*2

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="90vmin" height="90vmin" shape-rendering="crispEdges">`, total, total)
	fmt.Fprintf(&sb, `<rect width="%d" height="%d" fill="white"/>`, total, total)
	for y := range size {
		for x := range size {
			if code.Black(x, y) {
				fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="1" height="1" fill="black"/>`, x+pad, y+pad)
			}
		}
	}
	sb.WriteString(`</svg>`)

	html := "<!DOCTYPE html><html><head><meta charset='utf-8'>" +
		"<style>body{margin:0;background:#fff;display:flex;flex-direction:column;justify-content:center;align-items:center;height:100vh;font-family:sans-serif}" +
		"p{font-size:18px;margin-bottom:16px}</style></head><body>" +
		"<p>Telegram &#8594; Settings &#8594; Devices &#8594; Link Desktop Device</p>" +
		sb.String() +
		"</body></html>"

	f, err := os.CreateTemp("", "tg-qr-*.html")
	if err != nil {
		fmt.Println(token.URL())
		return
	}
	name := f.Name() + ".html"
	os.Rename(f.Name(), name)
	f.Close()
	os.WriteFile(name, []byte(html), 0644)

	switch runtime.GOOS {
	case "windows":
		exec.Command("cmd", "/c", "start", "", name).Start()
	case "darwin":
		exec.Command("open", name).Start()
	default:
		exec.Command("xdg-open", name).Start()
	}
}

func savePeers(peers map[int64]bool) error {
	ids := make([]int64, 0, len(peers))
	for id := range peers {
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return os.WriteFile(cacheFile, data, 0644)
}

func loadPeers() (map[int64]bool, error) {
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	peers := make(map[int64]bool, len(ids))
	for _, id := range ids {
		peers[id] = true
	}
	return peers, nil
}

func fetchUnmutedPeers(ctx context.Context, raw *tg.Client) (map[int64]bool, error) {
	unmuted := make(map[int64]bool)
	var offsetDate, offsetID int
	var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
	var total, processed int
	start := time.Now()

	for {
		res, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			Limit:      100,
			OffsetDate: offsetDate,
			OffsetID:   offsetID,
			OffsetPeer: offsetPeer,
		})
		if err != nil {
			if s := err.Error(); strings.Contains(s, "FLOOD_WAIT") {
				wait := 30
				if i := strings.Index(s, "FLOOD_WAIT ("); i >= 0 {
					rest := s[i+12:]
					if j := strings.Index(rest, ")"); j >= 0 {
						if n, e := strconv.Atoi(rest[:j]); e == nil {
							wait = n + 1
						}
					}
				}
				fmt.Printf("\nFLOOD_WAIT, жду %d сек...", wait)
				time.Sleep(time.Duration(wait) * time.Second)
				continue
			}
			return nil, err
		}

		var dialogs []tg.DialogClass
		var messages []tg.MessageClass

		switch d := res.(type) {
		case *tg.MessagesDialogs:
			dialogs, messages = d.Dialogs, d.Messages
			if total == 0 {
				total = len(dialogs)
			}
		case *tg.MessagesDialogsSlice:
			dialogs, messages = d.Dialogs, d.Messages
			if total == 0 {
				total = d.Count
			}
		}

		done := len(dialogs) < 100
		if !done {
			updated := false
			for i := len(messages) - 1; i >= 0; i-- {
				switch m := messages[i].(type) {
				case *tg.Message:
					offsetDate, offsetID = m.Date, m.ID
					updated = true
				case *tg.MessageService:
					offsetDate, offsetID = m.Date, m.ID
					updated = true
				}
				if updated {
					break
				}
			}
			if !updated {
				break
			}
		}

		for _, d := range dialogs {
			dialog, ok := d.(*tg.Dialog)
			if !ok {
				continue
			}
			if dialog.NotifySettings.MuteUntil != 0 {
				continue
			}
			switch p := dialog.Peer.(type) {
			case *tg.PeerUser:
				unmuted[p.UserID] = true
			case *tg.PeerChat:
				unmuted[p.ChatID] = true
			case *tg.PeerChannel:
				unmuted[p.ChannelID] = true
			}
		}

		processed += len(dialogs)
		elapsed := time.Since(start).Seconds()
		speed := 0.0
		if elapsed > 0 {
			speed = float64(processed) / elapsed
		}
		if total > 0 {
			fmt.Printf("\rДиалоги: %d / %d  (%.0f д/с)   ", processed, total, speed)
		} else {
			fmt.Printf("\rДиалоги: %d  (%.0f д/с)   ", processed, speed)
		}

		if done {
			fmt.Println()
			break
		}
	}

	fmt.Printf("Незамьюченных диалогов: %d\n", len(unmuted))
	return unmuted, nil
}

func main() {
	_ = godotenv.Load()
	refresh := len(os.Args) > 1 && os.Args[1] == "--refresh"

	apiID, err := strconv.Atoi(mustEnv("TG_API_ID"))
	if err != nil {
		panic("TG_API_ID must be a number: " + err.Error())
	}
	apiHash := mustEnv("TG_API_HASH")

	ctx := context.Background()
	var unmutedPeers map[int64]bool

	dispatcher := tg.NewUpdateDispatcher()
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		UpdateHandler:  dispatcher,
		SessionStorage: &telegram.FileSessionStorage{Path: "session.json"},
	})

	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok || msg.Out {
			return nil
		}
		var peerID int64
		switch p := msg.PeerID.(type) {
		case *tg.PeerUser:
			peerID = p.UserID
		case *tg.PeerChat:
			peerID = p.ChatID
		case *tg.PeerChannel:
			peerID = p.ChannelID
		}
		if unmutedPeers[peerID] {
			triggerHaptic()
			fmt.Printf("haptic peer_id=%d\n", peerID)
		}
		return nil
	})

	loggedIn := qrlogin.OnLoginToken(dispatcher)

	if err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			_, err := client.QR().Auth(ctx, loggedIn, func(ctx context.Context, token qrlogin.Token) error {
				showQR(token)
				return nil
			})
			if err != nil {
				if !errors.Is(err, auth.ErrPasswordAuthNeeded) && !strings.Contains(err.Error(), "SESSION_PASSWORD_NEEDED") {
					return err
				}
				if _, err := client.Auth().Password(ctx, mustEnv("TG_PASSWORD")); err != nil {
					return err
				}
			}
		}

		raw := tg.NewClient(client)
		cacheExists := func() bool {
			_, err := os.Stat(cacheFile)
			return err == nil
		}

		if refresh || !cacheExists() {
			fmt.Println("Загружаю диалоги с Telegram...")
			peers, err := fetchUnmutedPeers(ctx, raw)
			if err != nil {
				return err
			}
			unmutedPeers = peers
			if err := savePeers(peers); err != nil {
				fmt.Println("Не удалось сохранить кеш:", err)
			}
		} else {
			fmt.Println("Читаю кеш из", cacheFile)
			peers, err := loadPeers()
			if err != nil {
				fmt.Println("Не удалось прочитать кеш, загружаю с Telegram...")
				peers, err = fetchUnmutedPeers(ctx, raw)
				if err != nil {
					return err
				}
				savePeers(peers)
			}
			unmutedPeers = peers
		}

		fmt.Println("Слушаю сообщения...")
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		panic(err)
	}
}
