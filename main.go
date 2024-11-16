package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func generateQRCode(code string) error {
	cmd := exec.Command("qrencode", "-t", "ansiutf8", code)

	cmd.Stdout = os.Stdout

	return cmd.Run()
}

func getMessageText(message *waE2E.Message) string {
	if message.GetConversation() != "" {
		return message.GetConversation()
	}

	if message.ExtendedTextMessage.GetText() != "" {
		return message.ExtendedTextMessage.GetText()
	}

	return ""
}

func formatSenderJID(jid types.JID) types.JID {
	parts := strings.Split(jid.User, ":")
	formattedUser := parts[0]

	return types.NewJID(formattedUser, jid.Server)
}

func eventHandler(client *whatsmeow.Client) func(evt interface{}) {
	return func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			message := getMessageText(v.Message)
			if message == "/ping" {
				start := time.Now()

				phoneNumber := formatSenderJID(v.Info.Sender)

				messageID := v.Info.ID

				err := client.MarkRead(
					[]types.MessageID{messageID},
					time.Now(),
					phoneNumber,
					phoneNumber,
				)
				if err != nil {
					fmt.Println(err)
					return
				}

				err = client.SendPresence("available")
				if err != nil {
					fmt.Println(err)
					return
				}

				err = client.SendChatPresence(phoneNumber, "composing", "")
				if err != nil {
					fmt.Println(err)
					return
				}

				durationSleep := time.Duration(rand.Float64()*(2.5-1)+1) * time.Second
				time.Sleep(durationSleep)

				duration := time.Since(start)

				r, err := client.SendMessage(context.Background(), phoneNumber, &waE2E.Message{
					Conversation: proto.String(fmt.Sprintf("Pong! Tempo de resposta: %vms", duration.Abs().Milliseconds())),
				})
				if err != nil {
					fmt.Println(err)
					fmt.Println(r)
					return
				}

				client.SendChatPresence(phoneNumber, "paused", "")

				time.Sleep(1 * time.Second)

				client.SendPresence("unavailable")

				fmt.Println(phoneNumber)

				return
			}
		}
	}
}

func main() {
	dbLog := waLog.Stdout("Databse", "DEBUG", true)
	container, err := sqlstore.New("sqlite3", "file:store.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		panic(err)
	}

	//clientLog := waLog.Stdout("Client", "DEBUG", true)
	noOpLogger := waLog.Noop
	client := whatsmeow.NewClient(deviceStore, noOpLogger)
	client.AddEventHandler(eventHandler(client))

	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				fmt.Println("QR code:", evt.Code)
				generateQRCode(evt.Code)
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}
