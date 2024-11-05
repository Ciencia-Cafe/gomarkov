package main

import (
	"context"
	"math/rand/v2"
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
	usersExceptionsToDirectResponses := strings.Split(os.Getenv("DISCORD_USERS_EXCEPTIONS_TO_DIRECT_RESPONSES"), ",")
	discord, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		Error("error when connecting to discord: ", err)
		return
	}

	messageCount := 0
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
		var toks []int
		ConsumeMessage(&sequenceMap, message.Content, &toks)
		messages = append(messages, toks)
		_, err = MessageCollection.InsertOne(context.Background(), Message{
			CreatedAt: primitive.NewDateTimeFromTime(timestamp),
			AuthorId:  SnowflakeToUint64(message.Author.ID),
			Content:   message.Content,
		})
		CheckIrrelevantError(err)

		messageCount += 1
		if messageCount > 25 && 25+rand.IntN(50) < messageCount {
			var toks []string
			var finishedOk bool
			for tries := 0; tries < 5; tries += 1 {
				toks, finishedOk = GenerateTokensFromMessages(sequenceMap, messages, temp, []string{})
				if !finishedOk {
					continue
				}
				break
			}
			if finishedOk {
				str := StringFromTokens(toks)
				_, err := discord.ChannelMessageSendComplex(message.ChannelID, &discordgo.MessageSend{
					Content:         str,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
				})
				CheckIrrelevantError(err)
				messageCount = 0
			}
		}
	})

	discord.AddHandler(func(_ *discordgo.Session, interaction *discordgo.InteractionCreate) {
		messageCount = 0
		options := interaction.ApplicationCommandData().Options
		calleeUser := interaction.Interaction.Member.User

		shouldSendSeparate := slices.Contains(usersExceptionsToDirectResponses, calleeUser.ID)
		flags := discordgo.MessageFlagsEphemeral
		if !shouldSendSeparate {
			flags = 0
		}

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

				if shouldSendSeparate {
					_, err = discord.ChannelMessageSendComplex(interaction.ChannelID, &discordgo.MessageSend{
						Content:         str,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					})
					CheckIrrelevantError(err)
				}
				err = discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content:         str + "\n-# " + calleeUser.GlobalName + " usou /trigger",
						AllowedMentions: &discordgo.MessageAllowedMentions{},
						Flags:           flags,
					},
				})
				CheckIrrelevantError(err)
				break
			}
		case "autocomplete":
			{
				var startingToks []string
				optionRawValue := ""
				for _, option := range options {
					if option.Name == "text" {
						str := option.StringValue()
						startingToks = TokenizeString(str, nil, false)
						optionRawValue = str
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

				if shouldSendSeparate {
					_, err = discord.ChannelMessageSendComplex(interaction.ChannelID, &discordgo.MessageSend{
						Content:         str + "\n-# " + calleeUser.GlobalName + " usou /autocomplete " + optionRawValue,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					})
					CheckIrrelevantError(err)
				}
				err = discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content:         str,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
						Flags:           flags,
					},
				})
				CheckIrrelevantError(err)
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

				err = discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags: flags,
					},
				})
				CheckIrrelevantError(err)

				cursor, err := MessageCollection.Find(
					context.TODO(),
					bson.M{"AuthorId": SnowflakeToUint64(user.ID)},
					mongodbOptions.Find().SetProjection(bson.M{"_id": 0, "Content": 1}).SetBatchSize(16<<20))
				if err != nil {
					_, err = discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
						Content: "(erro) deu pau �",
					})
					CheckIrrelevantError(err)
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
					_, err = discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
						Content: "(erro) tenho mts poucas msgs dessa pessoa",
					})
					CheckIrrelevantError(err)
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

				if shouldSendSeparate {
					_, err = discord.ChannelMessageSendComplex(interaction.ChannelID, &discordgo.MessageSend{
						Content:         str + "\n-# " + calleeUser.GlobalName + " usou /impersonate " + user.Mention(),
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					})
					CheckIrrelevantError(err)
				}
				_, err = discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Content:         str + "\n-# impersonating " + user.GlobalName,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           flags,
				})
				CheckIrrelevantError(err)
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
	Info("online")
	<-signalChannel
	Info("shutting down")
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
