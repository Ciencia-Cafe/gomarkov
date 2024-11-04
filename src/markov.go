package main

import (
	"context"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Token = string

type WordRelations struct {
	Total     int
	Relations map[int]int
}

type SequenceMap = map[[SEQUENCE_SIZE]int]*WordRelations

const SEQUENCE_SIZE = 6
const MIN_SEQUENCE_SIZE = 2
const FIRST_TOKEN = "(first token)"
const LAST_TOKEN = "(last token)"
const INTERNED_FIRST_TOKEN = 1
const INTERNED_LAST_TOKEN = 2

var PUNCTUATIONS = [...]string{
	// "...",
	":",
	// ";",
	".",
	",",
	"?",
	"!",
	// "(",
	// ")",
	// "\n",
	// "\"",
	// "\t",
	"```",
}

var internedStrings = []string{"", FIRST_TOKEN, LAST_TOKEN}
var internedStringsMap = map[string]int{"": 0, FIRST_TOKEN: INTERNED_FIRST_TOKEN, LAST_TOKEN: INTERNED_LAST_TOKEN}

func internString(str string) (int, string) {
	if id, ok := internedStringsMap[str]; ok {
		return id, internedStrings[id]
	}

	str = strings.Clone(str)
	id := len(internedStrings)
	internedStrings = append(internedStrings, str)
	internedStringsMap[str] = id
	return id, str
}

func MakeSequenceMapFromMessages() (SequenceMap, bool) {
	ctx := context.Background()
	sequenceMap := make(SequenceMap)
	cursor, err := MessageCollection.Find(
		ctx,
		bson.M{},
		options.Find().SetBatchSize(16<<20).SetProjection(bson.M{"_id": 0, "Content": 1}))
	if err != nil {
		Error("failed to query all messages from collection:", err)
		return nil, false
	}
	defer cursor.Close(ctx)

	batchChannel := make(chan []Message)
	go ConsumeCursorToChannel(cursor, batchChannel)

	for batch := range batchChannel {
		for _, msg := range batch {
			if msg.Content != "" {
				ConsumeMessage(&sequenceMap, msg.Content, nil)
			}
		}

		Info("done with batch of length:", len(batch))
	}

	return sequenceMap, true
}

func ConsumeMessage(sequenceMap *SequenceMap, text string, outToks *[]int) {
	tokensArr := [256]Token{}
	tokens := tokensArr[:0:256]
	tokens = TokenizeString(text, tokens, true)

	seqArr := [SEQUENCE_SIZE]string{}
	seq := seqArr[:0:3]

	for _, tok := range tokens {
		if len(seq) < MIN_SEQUENCE_SIZE {
			getAndIncrementFromSeqmap(sequenceMap, sequenceFromSlice(seq), tok, 1)
		} else {
			for i := len(seq) - 1; i >= MIN_SEQUENCE_SIZE; i -= 1 {
				getAndIncrementFromSeqmap(sequenceMap, sequenceFromSlice(seq[i:]), tok, 1)
			}
		}
		// getAndIncrementFromSeqmap(&sequenceMap, sequenceFromSlice(seq), tok, 1)
		if len(seq) >= SEQUENCE_SIZE {
			for i := 0; i < SEQUENCE_SIZE-1; i += 1 {
				seq[i] = seq[i+1]
			}
			seq[SEQUENCE_SIZE-1] = tok
		} else {
			Append2(&seq, tok)
		}
	}

	seq = seq[:len(seq)-1]
	for len(seq) > 0 {
		getAndIncrementFromSeqmap(sequenceMap, sequenceFromSlice(seq), LAST_TOKEN, 1)
		seq = seq[1:]
	}

	if outToks != nil {
		toks := make([]int, len(tokens))
		for i, tok := range tokens {
			toks[i], _ = internString(tok)
		}
		*outToks = toks
	}
}

func TokenizeString(text string, tokens []Token, includeFirstAndLastToken bool) []Token {
	if includeFirstAndLastToken {
		Append2(&tokens, FIRST_TOKEN)
	}
	head := 0

outerLoop:
	for head < len(text) {
		for isWhitespace(text[head]) {
			head += 1
			if head >= len(text) {
				break outerLoop
			}
		}
		subtext := text[head:]

		if text[head] == '<' {
			end := strings.IndexByte(text[head+1:], '>')
			if end != -1 {
				Append2(&tokens, text[head:][:end+2])
				head += end + 2
				continue
			}
		}

		if strings.HasPrefix(subtext, "```") {
			end := strings.Index(text[head+3:], "```")
			if end != -1 {
				Append2(&tokens, text[head:][:end+6])
				head += end + 6
				continue
			}
		}

		if punct := startsWithPunctuation(text[head:]); punct != "" {
			Append2(&tokens, punct)
			head += len(punct)
			continue
		}

		start := head
		for !isWhitespace(text[head]) && startsWithPunctuation(text[head:]) == "" {
			head += 1
			if head >= len(text) {
				break
			}
		}
		Append2(&tokens, text[start:head])
	}
	if includeFirstAndLastToken {
		Append2(&tokens, LAST_TOKEN)
	}
	return tokens
}

func StringFromTokens(toks []string) string {
	result := ""
	isFirstIter := true

	for _, tok := range toks {
		if tok == FIRST_TOKEN || tok == LAST_TOKEN {
			continue
		}

		if startsWithPunctuation(tok) != "" {
			result += tok
		} else {
			if !isFirstIter {
				result += " "
			}
			isFirstIter = false
			result += tok
		}
	}

	return result
}

func GenerateTokensFromSequenceMap(seqmap SequenceMap, temp float64, beginning []string) ([]string, bool) {
	result := make([]string, 1, 1+len(beginning))
	result[0] = FIRST_TOKEN
	Append2(&result, beginning...)

	for len(result) < 50 && result[len(result)-1] != LAST_TOKEN {
		tail := result[max(len(result)-SEQUENCE_SIZE, 0):]
		var validSequences = make([]*WordRelations, 0, 6)
		var preferredSequences = make([]*WordRelations, 0, 6)

		for len(tail) > 0 {
			key := sequenceFromSlice(tail)
			sequence, ok := seqmap[key]
			if ok {
				Append2(&validSequences, sequence)
				if len(sequence.Relations) > 1 {
					Append2(&preferredSequences, sequence)
				} else if _, ok := sequence.Relations[INTERNED_LAST_TOKEN]; !ok {
					Append2(&preferredSequences, sequence)
				}
			}
			tail = tail[1:]
		}

		if len(validSequences) == 0 {
			break
		}

		slices.SortStableFunc(preferredSequences, func(a *WordRelations, b *WordRelations) int {
			return b.Total - a.Total
		})

		var sequence *WordRelations
		if len(preferredSequences) > 0 {
			sequence = preferredSequences[0]
			// sequence = preferredSequences[randomIntTempered(0, len(preferredSequences), temp)]
		} else {
			sequence = validSequences[randomIntTempered(0, len(validSequences), temp)]
		}

		randomWord := randomWordFromRelations(sequence, temp)
		if randomWord == "" {
			break
		}
		Append2(&result, randomWord)
	}

	finishedGracefully := true
	if result[len(result)-1] != LAST_TOKEN {
		Append2(&result, LAST_TOKEN)
		finishedGracefully = false
	}

	return result, finishedGracefully
}

func GenerateTokensFromMessages(seqmap SequenceMap, msgs [][]int, temp float64, beginning []string) ([]string, bool) {
	maxMessageCount := 2 + rand.IntN(2)
	alreadySeenMessages := make([]int, 0, maxMessageCount+1)

	var toks []int
	if len(beginning) > 0 {
		toks = make([]int, 1+len(beginning))
		toks[0] = INTERNED_FIRST_TOKEN
		for i, tok := range beginning {
			toks[1+i], _ = internString(tok)
		}
	} else {
		index := rand.IntN(len(msgs))
		msg := msgs[index]
		alreadySeenMessages = append(alreadySeenMessages, index)
		split := findBestSplitPoint2(seqmap, msg)
		if split != -1 {
			msg = msg[:split]
		}
		toks = slices.Clone(msg)
	}

	// generate tokens
	for toks[len(toks)-1] != INTERNED_LAST_TOKEN {
		tail := toks[max(len(toks)-SEQUENCE_SIZE, 0):]

		type MessageIndexPair struct {
			Index   int
			Message []int
		}
		filtered := make([]MessageIndexPair, 0)
		for ; len(filtered) == 0 && len(tail) > 0; tail = tail[1:] {
			for msgIndex, msg := range msgs {
				if slices.Contains(alreadySeenMessages, msgIndex) {
					continue
				}

				splitPoint := -1
				ok := false
				for !ok {
					newSplitPoint := slices.Index(msg[splitPoint+1:], tail[len(tail)-1])
					if newSplitPoint == -1 {
						break
					}
					splitPoint += newSplitPoint + 1
					temp := msg[max(splitPoint-len(tail)+1, 0):]
					if !slices.Equal(tail, temp[:min(len(tail), len(temp))]) {
						continue
					}
					ok = true
				}

				if ok && splitPoint+3 < len(msg) {
					filtered = append(filtered, MessageIndexPair{Index: msgIndex, Message: msg[splitPoint+1:]})
				}
			}
		}
		if len(filtered) <= 0 {
			break
		}

		msgIndex := rand.IntN(len(filtered))
		msg := filtered[msgIndex].Message
		alreadySeenMessages = append(alreadySeenMessages, filtered[msgIndex].Index)

		if maxMessageCount > 1 {
			maxMessageCount -= 1

			splitPoint := findBestSplitPoint2(seqmap, msg)
			if splitPoint != -1 {
				msg = msg[:splitPoint]
			}
		}

		toks = append(toks, msg...)

		if len(toks) > 50 {
			break
		}
	}

	if true {
		Info("Messages used (" + strconv.Itoa(len(alreadySeenMessages)) + ")")
		for _, msgIndex := range alreadySeenMessages {
			msg := msgs[msgIndex]
			strs := make([]string, len(msg))
			for i, tok := range msg {
				strs[i] = internedStrings[tok]
			}
			Info("\t", strs)
		}
	}

	// make final slice
	finalSize := len(toks) - 2
	if toks[len(toks)-1] != INTERNED_LAST_TOKEN {
		finalSize += 1
	}
	result := make([]string, finalSize)
	i := 0
	for _, tok := range toks {
		if tok == INTERNED_FIRST_TOKEN || tok == INTERNED_LAST_TOKEN {
			continue
		}
		result[i] = internedStrings[tok]
		i += 1
	}
	return result, toks[len(toks)-1] == INTERNED_LAST_TOKEN
}

// func findBestSplitPoint(_ SequenceMap, toks []int) int {
// 	// TODO
// 	baseIndex := 0
// 	maxIndex := len(toks)
// 	if toks[0] == INTERNED_FIRST_TOKEN {
// 		baseIndex += 1
// 	}
// 	if toks[len(toks)-1] == INTERNED_LAST_TOKEN {
// 		maxIndex -= 1
// 	}
// 	if len(toks) > 3 {
// 		maxIndex -= 1
// 	}
// 	return baseIndex + rand.IntN(maxIndex-baseIndex)
// }

func findBestSplitPoint2(seqmap SequenceMap, toks []int) int {
	// TODO
	bestSplitIndex := -1
	bestScore := 0
	for i := range toks {
		tail := toks[max(i-SEQUENCE_SIZE, 0):i]
		for ; len(tail) > 0; tail = tail[1:] {
			key := sequenceFromTokenSlice(tail)
			if sequence, ok := seqmap[key]; ok {
				score := 0

				if len(sequence.Relations) > 1 {
					score = sequence.Total
				} else if _, ok := sequence.Relations[INTERNED_LAST_TOKEN]; !ok {
					score = sequence.Total
				}

				if rel, ok := sequence.Relations[INTERNED_LAST_TOKEN]; ok && rel > sequence.Total/2 {
					score = 0
				}

				if score > bestScore {
					bestScore = score
					bestSplitIndex = i + 1
				}
			}
		}
	}

	if bestSplitIndex != -1 {
		return bestSplitIndex
	} else {
		Warn("couldn't find best split")
		baseIndex := 0
		maxIndex := len(toks)
		if toks[0] == INTERNED_FIRST_TOKEN {
			baseIndex += 1
		}
		if toks[len(toks)-1] == INTERNED_LAST_TOKEN {
			maxIndex -= 1
		}
		return baseIndex + (maxIndex-baseIndex)/2
	}
}

func getAndIncrementFromSeqmap(seqmap *SequenceMap, seq [SEQUENCE_SIZE]int, tok string, amount int) {
	sequenceMap := *seqmap

	sequence, ok := sequenceMap[seq]
	if !ok {
		sequence = &WordRelations{Total: 0, Relations: make(map[int]int)}
		sequenceMap[seq] = sequence
	}
	sequence.Total += amount

	tokId, _ := internString(tok)
	relations, ok := sequence.Relations[tokId]
	if !ok {
		sequence.Relations[tokId] = 1
	} else {
		sequence.Relations[tokId] = relations + 1
	}

	*seqmap = sequenceMap
}

func startsWithPunctuation(str string) string {
	for _, punct := range PUNCTUATIONS {
		if strings.HasPrefix(str, punct) {
			if len(str) <= len(punct) {
				return punct
			} else if isWhitespace(str[len(punct)]) {
				return punct
			}
		}
	}
	return ""
}

func randomWordFromRelations(sequence *WordRelations, temp float64) string {
	if len(sequence.Relations) == 0 {
		return ""
	}
	if len(sequence.Relations) == 1 {
		for key := range sequence.Relations {
			return internedStrings[key]
		}
	}

	type WordAmountPair struct {
		Word   int
		Amount int
	}
	relations := make([]WordAmountPair, 0, len(sequence.Relations))
	for key, value := range sequence.Relations {
		Append2(&relations, WordAmountPair{key, value})
	}
	slices.SortFunc(relations, func(left WordAmountPair, right WordAmountPair) int {
		return right.Amount - left.Amount
	})

	amounted := randomIntTempered(0, sequence.Total, temp)
	acc := 0
	for _, value := range relations {
		if amounted >= acc && amounted < acc+value.Amount {
			return internedStrings[value.Word]
		}
		acc += value.Amount
	}
	panic("what?")
}

func sequenceFromSlice(slice []string) [SEQUENCE_SIZE]int {
	if len(slice) > 3 {
		slice = slice[len(slice)-3:]
	}

	result := [SEQUENCE_SIZE]int{}
	for i := 0; i < len(slice); i += 1 {
		result[i], _ = internString(slice[i])
	}
	return result
}

func sequenceFromTokenSlice(slice []int) [SEQUENCE_SIZE]int {
	if len(slice) > 3 {
		slice = slice[len(slice)-3:]
	}

	result := [SEQUENCE_SIZE]int{}
	for i := 0; i < len(slice); i += 1 {
		result[i] = slice[i]
	}
	return result
}

func randomIntTempered(from int, to int, temp float64) int {
	v := math.Pow(rand.Float64(), temp)
	return from + int(v*float64(to-from))
}

func isWhitespace(ch uint8) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}
