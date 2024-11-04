package main

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
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
	Append2(&internedStrings, str)
	internedStringsMap[str] = id
	return id, str
}

func MakeSequenceMapFromMessages() (SequenceMap, bool) {
	ctx := context.Background()
	sequenceMap := make(SequenceMap)
	cursor, err := MessageCollection.Find(
		ctx,
		bson.D{},
		options.Find().SetBatchSize(16<<20).SetProjection(bson.D{{"_id", 0}, {"Content", 1}}))
	if err != nil {
		fmt.Println("failed to query all messages from collection: ", err)
		return nil, false
	}
	defer cursor.Close(ctx)

	batchChannel := make(chan []Message)
	go ConsumeCursorToChannel(cursor, batchChannel)

	for batch := range batchChannel {
		for _, msg := range batch {
			if msg.Content != "" {
				ConsumeMessage(&sequenceMap, msg.Content)
			}
		}

		fmt.Println("done with batch of length: ", len(batch))
	}

	return sequenceMap, true
}

func ConsumeMessage(sequenceMap *SequenceMap, text string) {
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

	for i, tok := range toks {
		if tok == FIRST_TOKEN || tok == LAST_TOKEN {
			continue
		}

		if startsWithPunctuation(tok) != "" {
			result += tok
		} else {
			if i > 0 {
				result += " "
			}
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

func randomIntTempered(from int, to int, temp float64) int {
	v := math.Pow(rand.Float64(), temp)
	return from + int(v*float64(to-from))
}

func isWhitespace(ch uint8) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}
