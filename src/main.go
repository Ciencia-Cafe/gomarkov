package main

import (
	"context"
	"flag"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime/pprof"
	"slices"
	"strconv"
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

const (
	METHOD_DEFAULT = iota
	METHOD_EXPERIMENT1
)

func main() {
	profilerFile := flag.String("cpuprofile", "", "profile cpu usage")
	flag.Parse()

	// debug.SetMemoryLimit(512 << 20)

	if *profilerFile != "" {
		file, err := os.Create(*profilerFile)
		if err != nil {
			Error("failed to create file specified in -cpuprofile flag:", err)
		} else {
			pprof.StartCPUProfile(file)
			defer pprof.StopCPUProfile()
		}
	}

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

		if message.ReferencedMessage != nil &&
			message.ReferencedMessage.Author != nil &&
			message.ReferencedMessage.Author.ID == discord.State.User.ID &&
			slices.Contains([]string{"explique", "explain"}, message.Content) {
			searchId := SnowflakeToUint64(message.ReferencedMessage.ID)
			if searchId == 0 {
				_, err := discord.ChannelMessageSendReply(message.ChannelID, "vish...", message.Reference())
				CheckIrrelevantError(err)
				return
			}
			messagesUsedEntriesLock.Lock()
			var used MessagesUsedEntry
			for _, value := range messagesUsedEntries {
				if value.ID == searchId {
					used = value
					break
				}
			}
			messagesUsedEntriesLock.Unlock()
			if used.ID == 0 {
				str := "esqueci"
				options := [...]string{
					"explicar esse tapa q vou dar na sua cara",
					"cabecao",
				}
				if strings.HasPrefix(message.ReferencedMessage.Content, "Mensagens usadas (") ||
					slices.Contains(options[:], message.ReferencedMessage.Content) ||
					message.ReferencedMessage.Content == "vish..." ||
					message.ReferencedMessage.Content == "isso ai eu tirei do cu msm" {
					str = ""
					if rand.IntN(100) < 5 {
						str = options[rand.IntN(len(options))]
					}
				}

				if str != "" {
					_, err := discord.ChannelMessageSendReply(message.ChannelID, str, message.Reference())
					CheckIrrelevantError(err)
				}
				return
			}
			if len(used.GlobalMessagesUsed) > 0 {
				str := "Mensagens usadas (" + strconv.Itoa(len(used.GlobalMessagesUsed)) + ")"
				for _, msgIndex := range used.GlobalMessagesUsed {
					msg := guildContext.AllMessages[msgIndex]
					str += "\n> " + StringFromTokens(msg) + "\n"
				}
				_, err := discord.ChannelMessageSendComplex(message.ChannelID, &discordgo.MessageSend{
					Content:         str,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Reference:       message.Reference(),
				})
				CheckIrrelevantError(err)
			} else if len(used.UserMessagesUsed) > 0 {
				// NOTE: go func so that the lock is released
				go func() {
					cursor, err := MessageCollection.Find(context.Background(), bson.M{
						"_id": bson.M{
							"$in": used.UserMessagesUsed,
						},
					}, mongodbOptions.Find().SetProjection(bson.M{
						"Content": 1,
					}))
					if err != nil {
						_, err := discord.ChannelMessageSendReply(message.ChannelID, "deu pau pra conferir o banco", message.Reference())
						CheckIrrelevantError(err)
					}

					ch := make(chan []Message)
					go ConsumeCursorToChannel(cursor, ch)
					messages := []Message{}
					for batch := range ch {
						messages = append(messages, batch...)
					}

					slices.SortFunc(messages, func(a Message, b Message) int {
						left := slices.Index(used.UserMessagesUsed, a.Id)
						right := slices.Index(used.UserMessagesUsed, b.Id)

						return left - right
					})

					str := "Mensagens usadas (" + strconv.Itoa(len(messages)) + ")"
					for _, msg := range messages {
						str += "\n> " + msg.Content + "\n"
					}
					_, err = discord.ChannelMessageSendComplex(message.ChannelID, &discordgo.MessageSend{
						Content:         str,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
						Reference:       message.Reference(),
					})
					CheckIrrelevantError(err)
				}()
			} else {
				_, err := discord.ChannelMessageSendReply(message.ChannelID, "isso ai eu tirei do cu msm", message.Reference())
				CheckIrrelevantError(err)
			}

			return
		}

		if slices.Contains(guildContext.GuildData.ScrapingChannelsIds, channelId) {
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
		}

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
				str, _ := generateText(guildContext.GlobalDict, guildContext.AllMessages, 0, nil, METHOD_DEFAULT)
				_, err := discord.ChannelMessageSendComplex(message.ChannelID, &discordgo.MessageSend{
					Content:         str,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
				})
				CheckIrrelevantError(err)
				perChannelMessageCounter[channelId] = 0
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

		method := METHOD_DEFAULT
		for _, option := range options {
			if option.Name == "method" {
				method = int(option.IntValue())
			}
		}

		shouldSendSeparate := slices.Contains(usersExceptionsToDirectResponses, calleeUser.ID)
		flags := discordgo.MessageFlagsEphemeral
		if !shouldSendSeparate {
			flags = 0
		}

		commandName := interaction.ApplicationCommandData().Name
		switch commandName {
		case "trigger":
			{
				err := discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						AllowedMentions: &discordgo.MessageAllowedMentions{},
						Flags:           flags,
					},
				})
				CheckIrrelevantError(err)

				str, messagesUsed := generateText(guildContext.GlobalDict, guildContext.AllMessages, 0, nil, method)

				if shouldSendSeparate {
					_, err = discord.ChannelMessageSendComplex(interaction.ChannelID, &discordgo.MessageSend{
						Content:         str,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					})
					CheckIrrelevantError(err)
				}
				msg, err := discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Content:         str + "\n-# " + calleeUser.GlobalName + " usou /trigger",
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           flags,
				})
				if err == nil {
					insertMessagesUsedEntry(MessagesUsedEntry{
						ID:                 SnowflakeToUint64(msg.ID),
						GlobalMessagesUsed: messagesUsed,
					})
				}
				CheckIrrelevantError(err)
				break
			}
		case "autocomplete":
			{
				var startingToks []Token
				optionRawValue := ""
				for _, option := range options {
					if option.Name == "text" {
						str := option.StringValue()
						startingToks = TokenizeString(str, nil, false)
						optionRawValue = str
					}
				}

				err := discord.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						AllowedMentions: &discordgo.MessageAllowedMentions{},
						Flags:           flags,
					},
				})
				CheckIrrelevantError(err)

				str, messagesUsed := generateText(guildContext.GlobalDict, guildContext.AllMessages, 0, startingToks, method)
				// Info("generated:", str)

				if shouldSendSeparate {
					_, err = discord.ChannelMessageSendComplex(interaction.ChannelID, &discordgo.MessageSend{
						Content:         str + "\n-# " + calleeUser.GlobalName + " usou /autocomplete " + optionRawValue,
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					})
					CheckIrrelevantError(err)
				}
				msg, err := discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Content:         str,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           flags,
				})
				CheckIrrelevantError(err)
				if err == nil {
					insertMessagesUsedEntry(MessagesUsedEntry{
						ID:                 SnowflakeToUint64(msg.ID),
						GlobalMessagesUsed: messagesUsed,
					})
				}
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
				})
				CheckIrrelevantError(err)

				cursor, err := MessageCollection.Find(
					context.TODO(),
					bson.M{
						"GuildId":  guildId,
						"AuthorId": SnowflakeToUint64(user.ID),
					},
					mongodbOptions.Find().SetProjection(bson.M{"Content": 1}).SetBatchSize(16<<20))
				if err != nil {
					Error("error when querying user messages:", err)
					_, err = discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
						Content: "deu pau �",
						Flags:   discordgo.MessageFlagsEphemeral,
					})
					CheckIrrelevantError(err)
					break
				}

				batchChannel := make(chan []Message)
				go ConsumeCursorToChannel(cursor, batchChannel)
				seqmap := make(SequenceMap)
				messages := make([][]Token, 0)
				messagesIds := make([]primitive.ObjectID, 0)

				for batch := range batchChannel {
					for _, msg := range batch {
						var toks []Token
						ConsumeMessage(&seqmap, msg.Content, &toks)
						messages = append(messages, toks)
						messagesIds = append(messagesIds, msg.Id)
					}
				}

				if len(messages) <= 50 {
					_, err = discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
						Content: "(erro) tenho mts poucas msgs dessa pessoa",
						Flags:   discordgo.MessageFlagsEphemeral,
					})
					CheckIrrelevantError(err)
					break
				}

				str, messagesUsed := generateText(seqmap, messages, 0, nil, method)

				if shouldSendSeparate {
					_, err = discord.ChannelMessageSendComplex(interaction.ChannelID, &discordgo.MessageSend{
						Content:         str + "\n-# " + calleeUser.GlobalName + " usou /impersonate " + user.Mention(),
						AllowedMentions: &discordgo.MessageAllowedMentions{},
					})
					CheckIrrelevantError(err)
				}
				msg, err := discord.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Content:         str + "\n-# impersonating " + user.GlobalName,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
					Flags:           flags,
				})
				CheckIrrelevantError(err)
				if err == nil {
					actualMessagesUsed := make([]primitive.ObjectID, len(messagesUsed))
					for i, msgIndex := range messagesUsed {
						actualMessagesUsed[i] = messagesIds[msgIndex]
					}

					insertMessagesUsedEntry(MessagesUsedEntry{
						ID:               SnowflakeToUint64(msg.ID),
						UserMessagesUsed: actualMessagesUsed,
					})
				}
			}
		}
	})

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGINT)

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

type MessagesUsedEntry struct {
	ID                 uint64
	GlobalMessagesUsed []int
	UserMessagesUsed   []primitive.ObjectID
}

var messagesUsedEntries = []MessagesUsedEntry{}
var messagesUsedEntriesLock = sync.RWMutex{}

func insertMessagesUsedEntry(entry MessagesUsedEntry) {
	if entry.ID == 0 {
		Error("trying to insert entry with ID 0:", entry)
		return
	}

	messagesUsedEntriesLock.Lock()
	defer messagesUsedEntriesLock.Unlock()

	const LIMIT = 1000
	if len(messagesUsedEntries) > LIMIT+1 {
		messagesUsedEntries = messagesUsedEntries[len(messagesUsedEntries)-LIMIT:]
	}

	messagesUsedEntries = append(messagesUsedEntries, entry)
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

func generateText(seqmap SequenceMap, messages [][]Token, temp float64, beginning []Token, method int) (string, []int) {
	if temp == 0 {
		if method == METHOD_DEFAULT {
			temp = 0.9
		} else if method == METHOD_EXPERIMENT1 {
			temp = 0.01
		}
	}

	var toks []Token
	var messagesUsed []int
	var finishedOk bool
	for tries := 0; tries < 5; tries += 1 {
		if method == METHOD_DEFAULT {
			toks, messagesUsed = GenerateTokensFromMessages(seqmap, messages, temp, beginning)
		} else if method == METHOD_EXPERIMENT1 {
			toks, messagesUsed = GenerateTokensFromSequenceMap2(seqmap, messages, temp, beginning, 25)
		}
		finishedOk = toks[len(toks)-1] == INTERNED_LAST_TOKEN

		if !finishedOk {
			continue
		}
		break
	}
	str := StringFromTokens(toks)
	if !finishedOk {
		str += " (...nn sei mais com oq completar)"
	}

	if true {
		Info("Messages used (" + strconv.Itoa(len(messagesUsed)) + ")")
		for _, msgIndex := range messagesUsed {
			msg := messages[msgIndex]
			strs := make([]string, len(msg))
			for i, tok := range msg {
				strs[i] = internedStrings[tok]
			}
			Info("\t", strs)
		}
	}

	return str, messagesUsed
}

var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "explain",
		Description: "mostra as msgs q usei pra fazer um texto",
	},
	{
		Name:        "trigger",
		Description: "vai me rodar",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "method",
				Description: "método",
				Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "default",
						Value: METHOD_DEFAULT,
					},
					{
						Name:  "experiment1",
						Value: METHOD_EXPERIMENT1,
					},
				},
			},
		},
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
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "method",
				Description: "método",
				Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "default",
						Value: METHOD_DEFAULT,
					},
					{
						Name:  "experiment1",
						Value: METHOD_EXPERIMENT1,
					},
				},
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
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "method",
				Description: "método",
				Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "default",
						Value: METHOD_DEFAULT,
					},
					{
						Name:  "experiment1",
						Value: METHOD_EXPERIMENT1,
					},
				},
			},
		},
	},
}
