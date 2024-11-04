package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {
	debug.SetMemoryLimit(512 << 20)

	// ========================================================
	// .env init
	if err := godotenv.Load(); err != nil {
		fmt.Println("error when loading dotenv: ", err)
		return
	}

	// ========================================================
	// Database
	if ok := InitDatabase(); !ok {
		return
	}
	defer DoneDatabase()

	// fmt.Println(MessageCollection.DeleteMany(context.TODO(), bson.D{{"Content", ""}}))

	temp := 0.95
	sequenceMap, ok := MakeSequenceMapFromMessages()
	if !ok {
		fmt.Println("failed to generate sequence map from messages")
		return
	}

	for i := 0; i < 20; i += 1 {
		toks, finishedOk := GenerateTokensFromSequenceMap(sequenceMap, temp, []string{})
		if len(toks) < randomIntTempered(6, 12, temp) {
			i -= 1
			continue
		}
		str := StringFromTokens(toks)
		if !finishedOk {
			str += "..."
		}
		fmt.Println(str)
	}

	// ========================================================
	// Discord
	allowedChannels := strings.Split(os.Getenv("DISCORD_ALLOWED_CHANNELS"), ",")
	discord, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		fmt.Println("error when connecting to discord: ", err)
		return
	}

	discord.AddHandler(func(_ *discordgo.Session, message *discordgo.MessageCreate) {
		if !slices.Contains(allowedChannels, message.ChannelID) {
			return
		}
		if message.Author.Bot {
			return
		}

		timestamp, err := discordgo.SnowflakeTimestamp(message.ID)
		if err != nil {
			fmt.Println("failed to get timestamp from snowflake, defaulting to time.Now()")
			timestamp = time.Now()
		}

		fmt.Println("msg: ", message.Content)
		ConsumeMessage(&sequenceMap, message.Content)
		MessageCollection.InsertOne(context.Background(), Message{
			CreatedAt: primitive.NewDateTimeFromTime(timestamp),
			AuthorId:  message.Author.ID,
			Content:   message.Content,
		})
	})

	discord.AddHandler(func(_ *discordgo.Session, interaction *discordgo.InteractionCreate) {
		options := interaction.ApplicationCommandData().Options

		switch interaction.ApplicationCommandData().Name {
		case "trigger":
			{
				var toks []string
				var finishedOk bool
				for tries := 0; tries < 5; tries += 1 {
					toks, finishedOk = GenerateTokensFromSequenceMap(sequenceMap, temp, []string{})
					if len(toks) < randomIntTempered(6, 12, temp) {
						if len(toks) <= 2 {
							tries -= 1
						}
						continue
					}
					break
				}
				str := StringFromTokens(toks)
				if !finishedOk {
					str += "..."
				}

				discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: str,
					},
				})
				break
			}
		case "autocomplete":
			{
				var startingToks []string
				for _, option := range options {
					if option.Name == "text" {
						str := option.StringValue()
						startingToks = TokenizeString(str, nil, false)
					}
				}

				var toks []string
				var finishedOk bool
				for tries := 0; tries < 5; tries += 1 {
					toks, finishedOk = GenerateTokensFromSequenceMap(sequenceMap, temp, startingToks)
					if len(toks) < randomIntTempered(6, 12, temp) {
						continue
					}
					break
				}
				str := StringFromTokens(toks)
				if !finishedOk {
					str += "..."
				}

				discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: str,
					},
				})
				break
			}
		}
	})

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	err = discord.Open()
	if err != nil {
		fmt.Println("error opening discord session: ", err)
		return
	}
	defer func() {
		fmt.Println("closing discord session...")
		discord.Close()
	}()

	for _, command := range commands {
		if _, err := discord.ApplicationCommandCreate(discord.State.User.ID, "", command); err != nil {
			fmt.Println("error registering application commands: ", err)
			return
		}
	}

	//
	<-signalChannel
}

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "trigger",
		Description: "vai me rodar",
	},
	{
		Name:        "autocomplete",
		Description: "me diz algo pra auto-completar",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "text",
				Description: "texto",
				Required:    true,
			},
		},
	},
}
