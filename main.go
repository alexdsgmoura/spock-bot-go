package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chai2010/webp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
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

/* func ConvertJPEGToWebP(input []byte, quality int) ([]byte, error) {
	inputFile, err := os.CreateTemp("", "input-*.jpg")
	if err != nil {
		return nil, err
	}
	defer os.Remove(inputFile.Name())

	_, err = inputFile.Write(input)
	if err != nil {
		return nil, err
	}

	err = inputFile.Sync()
	if err != nil {
		return nil, err
	}

	var outputBuffer bytes.Buffer
	var errorBuffer bytes.Buffer

	cmd := exec.Command("ffmpeg", "-i", inputFile.Name(), "-vf", "scale=512:512", "-q:v", fmt.Sprintf("%d", quality), "-f", "webp", "-")
	cmd.Stdout = &outputBuffer
	cmd.Stderr = &errorBuffer

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg error: %v, stderr: %s", err, errorBuffer.String())
	}

	return outputBuffer.Bytes(), nil
} */

func ConvertJPEGToWebP(input []byte, quality float32) ([]byte, error) {
	// Decodificar a imagem JPEG do slice de bytes
	img, err := jpeg.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, fmt.Errorf("erro ao decodificar JPEG: %v", err)
	}

	// Criar um buffer para armazenar a saída em WebP
	var outputBuffer bytes.Buffer

	// Codificar a imagem em formato WebP
	if err := webp.Encode(&outputBuffer, img, &webp.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("erro ao codificar WebP: %v", err)
	}

	return outputBuffer.Bytes(), nil
}

func AddExifToWebP(inputData []byte, packID, packName, authorName string) ([]byte, error) {
	// Constantes para os bytes iniciais e finais necessários
	startingBytes := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00, 0x41, 0x57, 0x07, 0x00}
	endingBytes := []byte{0x16, 0x00, 0x00, 0x00}
	var b bytes.Buffer

	// JSON de metadados
	jsonData := map[string]interface{}{
		"sticker-pack-id":        packID,
		"sticker-pack-name":      packName,
		"sticker-pack-publisher": authorName,
		"emojis":                 []string{"😀"},
	}

	jsonBytes, err := json.Marshal(jsonData)
	if err != nil {
		return nil, err
	}

	// Escrevendo os bytes iniciais e o comprimento dos metadados
	jsonLength := uint32(len(jsonBytes))
	lenBuffer := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuffer, jsonLength)

	if _, err := b.Write(startingBytes); err != nil {
		return nil, err
	}
	if _, err := b.Write(lenBuffer); err != nil {
		return nil, err
	}
	if _, err := b.Write(endingBytes); err != nil {
		return nil, err
	}
	if _, err := b.Write(jsonBytes); err != nil {
		return nil, err
	}

	// Caminhos temporários para os arquivos
	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, "input.webp")
	outputPath := filepath.Join(tempDir, "output.webp")
	exifPath := filepath.Join(tempDir, "exif.exif")

	// Salvando os arquivos temporários
	if err := os.WriteFile(inputPath, inputData, 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(exifPath, b.Bytes(), 0644); err != nil {
		return nil, err
	}
	defer os.Remove(inputPath)
	defer os.Remove(exifPath)
	defer os.Remove(outputPath)

	// Comando webpmux para inserir EXIF
	cmd := exec.Command("webpmux", "-set", "exif", exifPath, inputPath, "-o", outputPath)
	if err := cmd.Run(); err != nil {
		return nil, errors.New("falha ao executar o comando webpmux: " + err.Error())
	}

	// Retornando a imagem WebP final com EXIF
	return os.ReadFile(outputPath)
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
					v.Info.Chat,
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

				err = client.SendChatPresence(v.Info.Chat, "composing", "")
				if err != nil {
					fmt.Println(err)
					return
				}

				durationSleep := time.Duration(rand.Float64()*(3000-1000-1)+1000) * time.Millisecond
				time.Sleep(durationSleep)

				duration := time.Since(start)

				r, err := client.SendMessage(context.Background(), v.Info.Chat, &waE2E.Message{
					Conversation: proto.String(fmt.Sprintf("Pong! Tempo de resposta: %vms", duration.Abs().Milliseconds())),
				})
				if err != nil {
					fmt.Println(err)
					fmt.Println(r)
					return
				}

				client.SendChatPresence(v.Info.Chat, "paused", "")
				time.Sleep(1 * time.Second)
				client.SendPresence("unavailable")

				fmt.Println("Mensagem enviada para:", phoneNumber.User)
			}

			if v.Info.IsGroup {
				if message == "/fig" {
					start := time.Now()

					if v.Message.GetExtendedTextMessage() != nil && v.Message.GetExtendedTextMessage().ContextInfo.GetStanzaID() != "" {
						if v.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage().GetImageMessage() != nil {
							fmt.Println(*v.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage().GetImageMessage().Mimetype)

							downloadedImage, err := client.Download(v.Message.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage().GetImageMessage())
							if err != nil {
								fmt.Println(err)
								return
							}

							webPImg, err := ConvertJPEGToWebP(downloadedImage, 100)
							if err != nil {
								fmt.Println(err)
								return
							}

							webPWithExif, err := AddExifToWebP(webPImg, "com.snowcorp.stickerly.android.stickercontentprovider 2f44112f-1143-49e7-8ff7-ba18595760a3", "kakaka", "Sticker.ly * glauber_viniciusff")
							if err != nil {
								fmt.Println("Erro ao adicionar EXIF:", err)
								return
							}

							resp, err := client.Upload(context.Background(), webPWithExif, whatsmeow.MediaImage)
							if err != nil {
								fmt.Println(err)
								return
							}

							_, err = client.SendMessage(context.Background(), formatSenderJID(v.Info.Chat), &waE2E.Message{
								StickerMessage: &waE2E.StickerMessage{
									Mimetype:      proto.String("image/webp"),
									URL:           &resp.URL,
									MediaKey:      resp.MediaKey,
									FileEncSHA256: resp.FileEncSHA256,
									FileSHA256:    resp.FileSHA256,
									FileLength:    &resp.FileLength,
									Height:        proto.Uint32(80),
									Width:         proto.Uint32(80),
									PngThumbnail:  webPImg,
									ContextInfo: &waE2E.ContextInfo{
										StanzaID:      &v.Info.ID,
										Participant:   proto.String(formatSenderJID(v.Info.Sender).String()),
										QuotedMessage: v.Message,
									},
								},
							})
							if err != nil {
								fmt.Printf("Erro ao enviar mensagem: %v\n", err)
								return
							}

							return
						}

						err := client.SendPresence("available")
						if err != nil {
							fmt.Println(err)
							return
						}

						err = client.MarkRead(
							[]types.MessageID{v.Info.ID},
							time.Now(),
							v.Info.Chat,
							formatSenderJID(v.Info.Sender),
						)
						if err != nil {
							fmt.Println(err)
							return
						}

						err = client.SendChatPresence(v.Info.Chat, "composing", "")
						if err != nil {
							fmt.Println(err)
							return
						}

						durationSleep := time.Duration(rand.Float64()*(3000-1000-1)+1000) * time.Millisecond
						time.Sleep(durationSleep)
						duration := time.Since(start)

						client.SendMessage(context.Background(), formatSenderJID(v.Info.Chat), &waE2E.Message{
							ExtendedTextMessage: &waE2E.ExtendedTextMessage{
								Text: proto.String(fmt.Sprintf("> A mensagem respondida não é uma imagem!\r\n\r\nTempo de execução: %vms", duration.Abs().Milliseconds())),
								ContextInfo: &waE2E.ContextInfo{
									StanzaID:      &v.Info.ID,
									Participant:   proto.String(formatSenderJID(v.Info.Sender).String()),
									QuotedMessage: v.Message,
								},
							},
						})

						client.SendChatPresence(v.Info.Chat, "paused", "")
						time.Sleep(time.Duration(rand.Float64()*(3000-1000-1)+1000) * time.Millisecond)
						client.SendPresence("unavailable")

						return
					} else {
						err := client.SendPresence("available")
						if err != nil {
							fmt.Println(err)
							return
						}

						err = client.MarkRead(
							[]types.MessageID{v.Info.ID},
							time.Now(),
							v.Info.Chat,
							formatSenderJID(v.Info.Sender),
						)
						if err != nil {
							fmt.Println(err)
							return
						}

						err = client.SendChatPresence(v.Info.Chat, "composing", "")
						if err != nil {
							fmt.Println(err)
							return
						}

						durationSleep := time.Duration(rand.Float64()*(3000-1000-1)+1000) * time.Millisecond
						time.Sleep(durationSleep)
						duration := time.Since(start)

						client.SendMessage(context.Background(), formatSenderJID(v.Info.Chat), &waE2E.Message{
							ExtendedTextMessage: &waE2E.ExtendedTextMessage{
								Text: proto.String(fmt.Sprintf("> É necessário responder a imagem que deseja transformar em figurinha!\r\n\r\nTempo de execução: %vms", duration.Abs().Milliseconds())),
								ContextInfo: &waE2E.ContextInfo{
									StanzaID:      &v.Info.ID,
									Participant:   proto.String(formatSenderJID(v.Info.Sender).String()),
									QuotedMessage: v.Message,
								},
							},
						})

						client.SendChatPresence(v.Info.Chat, "paused", "")
						time.Sleep(time.Duration(rand.Float64()*(3000-1000-1)+1000) * time.Millisecond)
						client.SendPresence("unavailable")

						return
					}
				}
			}
		}
	}
}

func main() {
	dbLog := waLog.Stdout("Database", "DEBUG", true)

	sqlDB, err := sql.Open("sqlite", "file:store.db")
	if err != nil {
		panic(fmt.Errorf("falha ao abrir o banco de dados: %w", err))
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		panic(fmt.Errorf("falha ao ativar foreign keys: %w", err))
	}

	container := sqlstore.NewWithDB(sqlDB, "sqlite", dbLog)

	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		panic(fmt.Errorf("falha ao obter o dispositivo: %w", err))
	}

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
