package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongodbOptions "go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	debug.SetMemoryLimit(512 << 20)

	// ========================================================
	// .env init
	if err := godotenv.Load(); err != nil {
		Error("error when loading dotenv: ", err)
		return
	}

	// ========================================================
	// Database
	if ok := InitDatabase(); !ok {
		return
	}
	defer DoneDatabase()

	// fmt.Println(MessageCollection.DeleteMany(context.TODO(), bson.D{{"Content", ""}}))

	temp := 0.90
	var sequenceMap SequenceMap
	messages := make([][]int, 0)

	{
		sequenceMap = make(SequenceMap)
		cursor, err := MessageCollection.Find(
			context.TODO(),
			bson.D{},
			mongodbOptions.Find().SetBatchSize(16<<20).SetProjection(bson.M{"_id": 0, "Content": 1}))
		if err != nil {
			Error("failed to query all messages from collection:", err)
		} else {
			defer cursor.Close(context.TODO())

			batchChannel := make(chan []Message)
			go ConsumeCursorToChannel(cursor, batchChannel)

			for batch := range batchChannel {
				for _, msg := range batch {
					if msg.Content != "" {
						var toks []int
						ConsumeMessage(&sequenceMap, msg.Content, &toks)
						messages = append(messages, toks)
					}
				}

				Info("done with batch of length:", len(batch))
			}
		}
	}

	// for i := 0; i < 20; i += 1 {
	// 	toks, finishedOk := GenerateTokensFromSequenceMap(sequenceMap, temp, []string{})
	// 	// if len(toks) < randomIntTempered(6, 12, temp) {
	// 	// 	i -= 1
	// 	// 	continue
	// 	// }
	// 	str := StringFromTokens(toks)
	// 	if !finishedOk {
	// 		str += "..."
	// 	}
	// 	fmt.Println(str)
	// }

	// ========================================================
	// Discord
	allowedChannels := strings.Split(os.Getenv("DISCORD_ALLOWED_CHANNELS"), ",")
	discord, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		Error("error when connecting to discord: ", err)
		return
	}

	discord.AddHandler(func(_ *discordgo.Session, message *discordgo.MessageCreate) {
		if !slices.Contains(allowedChannels, message.ChannelID) {
			return
		}
		if message.Author.Bot {
			return
		}
		if len(message.Content) <= 0 {
			return
		}

		timestamp, err := discordgo.SnowflakeTimestamp(message.ID)
		if err != nil {
			Warn("failed to get timestamp from snowflake, defaulting to time.Now()")
			timestamp = time.Now()
		}

		Info("msg:", message.Content)
		ConsumeMessage(&sequenceMap, message.Content, nil)
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
					toks, finishedOk = GenerateTokensFromMessages(sequenceMap, messages, temp, []string{})
					if !finishedOk {
						continue
					}
					break
				}
				str := StringFromTokens(toks)
				if !finishedOk {
					str += " (...nn sei mais com oq completar)"
				}

				discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content:         str,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
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
					toks, finishedOk = GenerateTokensFromMessages(sequenceMap, messages, temp, startingToks)
					if !finishedOk {
						continue
					}
					break
				}
				str := StringFromTokens(toks)
				if !finishedOk {
					str += " (...nn sei mais com oq completar)"
				}

				discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content:         str,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					},
				})
				break
			}
		case "impersonate":
			{
				var user *discordgo.User
				for _, option := range options {
					if option.Name == "user" {
						user = option.UserValue(discord)
					}
				}

				discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
				})

				cursor, err := MessageCollection.Find(
					context.TODO(),
					bson.M{"AuthorId": user.ID},
					mongodbOptions.Find().SetProjection(bson.M{"_id": 0, "Content": 1}).SetBatchSize(16<<20))
				if err != nil {
					discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
						Content: "(erro) deu pau �",
					})
					break
				}

				batchChannel := make(chan []Message)
				go ConsumeCursorToChannel(cursor, batchChannel)
				seqmap := make(SequenceMap)
				messages := make([][]int, 0)

				for batch := range batchChannel {
					for _, msg := range batch {
						var toks []int
						ConsumeMessage(&seqmap, msg.Content, &toks)
						messages = append(messages, toks)
					}
				}

				if len(messages) <= 30 {
					discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
						Content: "(erro) tenho mts poucas msgs dessa pessoa",
					})
					break
				}

				var toks []string
				var finishedOk bool
				for tries := 0; tries < 5; tries += 1 {
					toks, finishedOk = GenerateTokensFromMessages(seqmap, messages, temp, []string{})
					if !finishedOk {
						continue
					}
					break
				}
				str := StringFromTokens(toks)
				if !finishedOk {
					str += " (...nn sei mais com oq completar)"
				}

				discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Content:         str,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
				})
			}
		}
	})

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	err = discord.Open()
	if err != nil {
		Error("error opening discord session:", err)
		return
	}
	defer func() {
		Info("closing discord session...")
		if err := discord.Close(); err != nil {
			Error("error closing discord session:", err)
		}
	}()

	for _, command := range commands {
		if _, err := discord.ApplicationCommandCreate(discord.State.User.ID, "", command); err != nil {
			Error("error registering application commands:", err)
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
	{
		Name:        "impersonate",
		Description: "me diz alguém pra impersonar",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "user",
				Description: "usuário",
				Required:    true,
			},
		},
	},
}
