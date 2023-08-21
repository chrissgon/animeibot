package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/chrissgon/goanime"
	"github.com/chrissgon/gomanga"
	"github.com/chrissgon/lowbot"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/machinebox/progress"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	ffprobe "github.com/vansante/go-ffprobe"
)

var (
	wg          sync.WaitGroup
	stopControl = map[string]bool{}
	maxUploadMb = 30
)

var channels = []func() (lowbot.Channel, error){
	lowbot.NewTelegram,
}

func init() {
	godotenv.Load()
	prepareFolders([]string{"out", os.Getenv("GOANIME_FOLDER")})
}

func prepareFolders(folders []string) {
	for _, folder := range folders {
		_, err := os.Stat(folder)

		if os.IsNotExist(err) {
			os.Mkdir(folder, 0755)
		}
	}
}

func main() {
	lowbot.EnableLocalPersist = false

	lowbot.SetCustomActions(getActions())
	flow, _ := lowbot.NewFlow("flow.yaml")
	persist, _ := lowbot.NewLocalPersist()

	for _, fn := range channels {
		channel, _ := fn()
		go lowbot.StartBot(flow, channel, persist)
	}

	wg.Add(1)
	wg.Wait()
}

func getActions() lowbot.ActionsMap {
	return lowbot.ActionsMap{
		"Manga": func(flow *lowbot.Flow, channel lowbot.Channel) (bool, error) {
			step := flow.Current
			lowbot.ActionText(flow, channel)

			images, err := gomanga.SearchByProviders(getNameAndNumber(step.GetLastResponseText()))

			if err != nil {
				flow.Current = flow.Steps["error"]
				return false, err
			}

			go func() {
				for _, image := range images {
					if stopControl[flow.SessionID] {
						return
					}

					time.Sleep(1 * time.Second)

					channel.SendImage(lowbot.NewInteractionMessageImage(flow.SessionID, image, ""))
				}

				flow.Current = flow.Steps["end"]
				lowbot.RunAction(flow, channel)
			}()

			return false, nil
		},
		"Anime": func(flow *lowbot.Flow, channel lowbot.Channel) (bool, error) {
			step := flow.Current
			lowbot.ActionText(flow, channel)

			status := make(chan progress.Progress)

			anime, episode := getNameAndNumber(step.GetLastResponseText())

			// anime progress rountine
			go func() {
				calls := 1
				first := true

				for s := range status {
					if stopControl[flow.SessionID] {
						return
					}

					percent := int(s.Percent())

					if first {
						first = false
						channel.SendText(lowbot.NewInteractionMessageText(flow.SessionID, "⬇️ Iniciando o download"))
						continue
					}

					if percent > calls*25 || percent == 100 {
						calls++
						message := fmt.Sprintf("✅ %d%% concluído!", percent)
						channel.SendText(lowbot.NewInteractionMessageText(flow.SessionID, message))
					}
				}
			}()

			go func() {
				file, err := goanime.DownloadByScraper(goanime.NewScraper(goanime.ANIMESONLINEHD, anime, episode, false), status)

				if stopControl[flow.SessionID] {
					return
				}

				if err != nil {
					flow.Current = flow.Steps["error"]
					lowbot.RunAction(flow, channel)
					lowbot.RunAction(flow.End(), channel)
					return
				}

				data, _ := ffprobe.GetProbeData(file, 120000*time.Millisecond)

				sizeBytes, _ := strconv.Atoi(data.Format.Size)
				sizeMb := sizeBytes / (1 << 20)
				parts := sizeMb/30 + 1

				duration := int(data.Format.Duration().Seconds() / 60)
				rangeMinutes := duration/parts + 1

				for sub := duration; sub > 0; sub = sub - rangeMinutes {
					start := duration - sub

					filePartName := fmt.Sprintf("./out/%s.mp4", uuid.NewString())

					ffInput := ffmpeg.Input(file, ffmpeg.KwArgs{"ss": 60 * start, "t": 60 * rangeMinutes})
					ffOutput := ffInput.Output(filePartName, ffmpeg.KwArgs{"c": "copy"})
					err := ffOutput.OverWriteOutput().Run()

					if err != nil {
						return
					}

					channel.SendVideo(lowbot.NewInteractionMessageVideo(flow.SessionID, filePartName, ""))

					os.Remove(filePartName)
				}

				os.Remove(file)

				flow.Current = flow.Steps["end"]
				lowbot.RunAction(flow, channel)
			}()

			return false, nil
		},
		"Stop": func(flow *lowbot.Flow, channel lowbot.Channel) (bool, error) {
			stopControl[flow.SessionID] = true
			time.Sleep(2 * time.Second)
			return true, nil
		},
		"End": func(flow *lowbot.Flow, channel lowbot.Channel) (bool, error) {
			delete(stopControl, flow.SessionID)
			return true, nil
		},
	}
}

func getNameAndNumber(value string) (string, string) {
	regex := regexp.MustCompile(`(.+) ([0-9]{1,})`)
	parts := regex.FindStringSubmatch(value)
	return parts[1], parts[2]
}
