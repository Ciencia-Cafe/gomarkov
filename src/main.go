package main

import (
	"context"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongodbOptions "go.mongodb.org/mongo-driver/mongo/options"
)

const DEFAULT_TEMP = 0.9

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

	updateGuildContexts()

	// Info(GuildCollection.InsertOne(context.TODO(), &Guild{
	// 	GuildId:                uint64(739580670480744490),
	// 	ScrapingChannelsIds:    []uint64{739582072980504628},
	// 	InteractionChannelsIds: []uint64{739582072980504628},
	// }))
	// Info(MessageCollection.UpdateMany(context.Background(), bson.M{}, bson.M{
	// 	"$set": bson.M{
	// 		"GuildId": uint64(739580670480744490),
	// 	},
	// }))
	// Info(GuildCollection.InsertOne(context.TODO(), &Guild{
	// 	GuildId:                uint64(507550989629521922),
	// 	ScrapingChannelsIds:    []uint64{553933292542361601, 507550989629521924, 835316057014927421, 671327942420201492},
	// 	InteractionChannelsIds: []uint64{671327942420201492},
	// }))

	// fmt.Println(MessageCollection.DeleteMany(context.TODO(), bson.D{{"Content", ""}}))

	{
		cursor, err := MessageCollection.Find(
			context.TODO(),
			bson.D{},
			mongodbOptions.Find().SetBatchSize(16<<20).SetProjection(
				bson.M{
					"_id":     0,
					"GuildId": 1,
					"Content": 1,
				},
			))
		if err != nil {
			Error("failed to query all messages from collection:", err)
		} else {
			batchChannel := make(chan []Message)
			go ConsumeCursorToChannel(cursor, batchChannel)

			for batch := range batchChannel {
				for _, msg := range batch {
					if msg.Content == "" {
						continue
					}
					guildContext, ok := guildContexts[msg.GuildId]
					if !ok {
						continue
					}

					var toks []Token
					ConsumeMessage(&guildContext.GlobalDict, msg.Content, &toks)
					guildContext.AllMessages = append(guildContext.AllMessages, toks)
					guildContexts[msg.GuildId] = guildContext
				}

				Info("done with batch of length:", len(batch))
			}
		}
	}

	guildsUpdateTicker := time.NewTicker(30 * time.Second)
	guildsUpdaterChannel := make(chan struct{})
	go func() {
		for {
			select {
			case <-guildsUpdateTicker.C:
				updateGuildContexts()
			case <-guildsUpdaterChannel:
				guildsUpdateTicker.Stop()
				return
			}
		}
	}()
	defer close(guildsUpdaterChannel)

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
	usersExceptionsToDirectResponses := strings.Split(os.Getenv("DISCORD_USERS_EXCEPTIONS_TO_DIRECT_RESPONSES"), ",")
	discord, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		Error("error when connecting to discord: ", err)
		return
	}

	perChannelMessageCounter := map[uint64]int32{}
	discord.AddHandler(func(_ *discordgo.Session, message *discordgo.MessageCreate) {
		if message.Author.Bot {
			return
		}
		if len(message.Content) <= 0 {
			return
		}

		guildContextsLock.RLock()
		defer guildContextsLock.RUnlock()

		guildId := SnowflakeToUint64(message.GuildID)
		channelId := SnowflakeToUint64(message.ChannelID)
		userId := SnowflakeToUint64(message.Author.ID)

		guildContext, ok := guildContexts[guildId]
		if !ok {
			return
		}
		defer func() {
			guildContexts[guildContext.GuildData.GuildId] = guildContext
		}()
		if !slices.Contains(guildContext.GuildData.ScrapingChannelsIds, channelId) {
			return
		}

		timestamp, err := discordgo.SnowflakeTimestamp(message.ID)
		if err != nil {
			Warn("failed to get timestamp from snowflake, defaulting to time.Now()")
			timestamp = time.Now()
		}

		Info("msg:", message.Content)
		var toks []Token
		ConsumeMessage(&guildContext.GlobalDict, message.Content, &toks)
		guildContext.AllMessages = append(guildContext.AllMessages, toks)
		_, err = MessageCollection.InsertOne(context.Background(), Message{
			CreatedAt: primitive.NewDateTimeFromTime(timestamp),
			GuildId:   guildId,
			ChannelId: channelId,
			AuthorId:  userId,
			Content:   message.Content,
		})
		CheckIrrelevantError(err)

		if slices.Contains(guildContext.GuildData.InteractionChannelsIds, channelId) {
			var messageCount int32
			messageCount, ok = perChannelMessageCounter[channelId]
			if !ok {
				messageCount = 0
			}
			messageCount += 1
			perChannelMessageCounter[channelId] = messageCount

			minCount := guildContext.GuildData.MinMessageCountToSendMessage
			maxCount := guildContext.GuildData.MaxMessageCountToSendMessage
			if minCount == 0 {
				minCount = 25
			}
			if maxCount == 0 {
				maxCount = minCount + 50
			}

			if messageCount >= minCount && minCount+rand.Int32N(maxCount-minCount) < messageCount {
				var toks []string
				var finishedOk bool
				for tries := 0; tries < 5; tries += 1 {
					toks, finishedOk = GenerateTokensFromMessages(guildContext.GlobalDict, guildContext.AllMessages, DEFAULT_TEMP, []string{})
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
					perChannelMessageCounter[channelId] = 0
				}
			}
		}
	})

	discord.AddHandler(func(_ *discordgo.Session, interaction *discordgo.InteractionCreate) {
		if interaction.Interaction.GuildID == "" {
			err := discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content:         "só posso ser usado em servidores específicos",
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           discordgo.MessageFlagsEphemeral,
				},
			})
			CheckIrrelevantError(err)
			return
		}
		if interaction.Interaction.ChannelID == "" {
			err := discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content:         "só posso ser usado em servidores específicos",
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           discordgo.MessageFlagsEphemeral,
				},
			})
			CheckIrrelevantError(err)
			return
		}

		guildId := SnowflakeToUint64(interaction.Interaction.GuildID)
		channelId := SnowflakeToUint64(interaction.Interaction.ChannelID)
		guildContextsLock.RLock()
		guildContext, ok := guildContexts[guildId]
		guildContextsLock.RUnlock()
		if !ok {
			err := discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content:         "só posso ser usado em servidores específicos",
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           discordgo.MessageFlagsEphemeral,
				},
			})
			CheckIrrelevantError(err)
			return
		}
		if !slices.Contains(guildContext.GuildData.InteractionChannelsIds, channelId) {
			err := discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content:         "só posso ser usado em canais específicos",
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           discordgo.MessageFlagsEphemeral,
				},
			})
			CheckIrrelevantError(err)
			return
		}

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
					toks, finishedOk = GenerateTokensFromMessages(guildContext.GlobalDict, guildContext.AllMessages, DEFAULT_TEMP, []string{})
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
					toks, finishedOk = GenerateTokensFromMessages(guildContext.GlobalDict, guildContext.AllMessages, DEFAULT_TEMP, startingToks)
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
					bson.M{
						"GuildId":  guildId,
						"AuthorId": SnowflakeToUint64(user.ID),
					},
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
				messages := make([][]Token, 0)

				for batch := range batchChannel {
					for _, msg := range batch {
						var toks []Token
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
					toks, finishedOk = GenerateTokensFromMessages(seqmap, messages, DEFAULT_TEMP, []string{})
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
	// bytes, err := json.Marshal(internedStrings)
	// if err == nil {
	// 	Info(os.WriteFile("./temp.json", bytes, 0))
	// } else {
	// 	Warn(err)
	// }
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

type GuildContext struct {
	GuildData   Guild
	GlobalDict  SequenceMap
	AllMessages [][]Token
}

var guildContexts = map[uint64]GuildContext{}
var guildContextsLock = sync.RWMutex{}

func updateGuildContexts() {
	cursor, err := GuildCollection.Find(context.Background(), bson.M{})
	if err != nil {
		Error("failed to update guilds contexts:", err)
		return
	}

	batchChannel := make(chan []Guild)
	go ConsumeCursorToChannel(cursor, batchChannel)

	allFoundGuilds := []Guild{}
	for batch := range batchChannel {
		allFoundGuilds = append(allFoundGuilds, batch...)
	}

	guildContextsLock.Lock()
	defer guildContextsLock.Unlock()

	newGuildContexts := map[uint64]GuildContext{}
	for _, guild := range allFoundGuilds {
		prev, ok := guildContexts[guild.GuildId]
		if ok {
			prev.GuildData = guild
			newGuildContexts[guild.GuildId] = prev
		} else {
			newGuildContexts[guild.GuildId] = GuildContext{
				GuildData:   guild,
				GlobalDict:  SequenceMap{},
				AllMessages: [][]Token{},
			}
		}
	}

	guildContexts = newGuildContexts
}
