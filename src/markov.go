package main

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type Token uint32

const MAX_TOKEN = math.MaxUint32

type WordRelations struct {
	Total     uint
	Relations map[Token]uint32
}

type SequenceMap map[[SEQUENCE_SIZE]Token]WordRelations

const SEQUENCE_SIZE = 6
const MIN_SEQUENCE_SIZE = 2
const FIRST_TOKEN = "(first token)"
const LAST_TOKEN = "(last token)"
const INTERNED_FIRST_TOKEN = 1
const INTERNED_LAST_TOKEN = 2

var PUNCTUATIONS = [...]string{
	":",
	// ";",
	"\"",
	"...",
	"..",
	".",
	",",
	"??",
	"?!",
	"?",
	"!!!",
	"!?",
	"!!",
	"!",
	"(",
	")",
	// "\n",
	// "\"",
	// "\t",
	"```",
}

var PUNCT_BINDING_RIGHT = [...]string{
	"(",
}

var PUNCT_BINDING_BOTH = [...]string{
	"\"",
}

var internedStrings []string
var internedStringsMap map[string]Token
var internedStringsMapLock sync.RWMutex

func init() {
	internedStrings = make([]string, 3, 200_000)
	internedStrings[0] = ""
	internedStrings[1] = FIRST_TOKEN
	internedStrings[2] = LAST_TOKEN
	internedStringsMap = make(map[string]Token, 200_000)
	for i, v := range internedStrings {
		internedStringsMap[v] = Token(i)
	}
}

func internString(str string) Token {
	internedStringsMapLock.RLock()
	id, ok := internedStringsMap[str]
	internedStringsMapLock.RUnlock()
	if ok {
		return id
	}

	internedStringsMapLock.Lock()
	defer internedStringsMapLock.Unlock()

	str = strings.Clone(str)
	id2 := len(internedStrings)
	if id2 > MAX_TOKEN {
		panic("interned strings map size would grow beyond MAX_TOKEN (" + strconv.Itoa(MAX_TOKEN) + ")")
	}
	id = Token(id2)
	internedStrings = append(internedStrings, str)
	internedStringsMap[str] = id
	return id
}

func ConsumeMessage(sequenceMap *SequenceMap, text string, outToks *[]Token) {
	tokensArr := [256]Token{}
	tokens := tokensArr[:0:256]
	tokens = TokenizeString(text, tokens, true)

	seqArr := [SEQUENCE_SIZE]Token{}
	seq := seqArr[:0:3]

	for _, tok := range tokens {
		if len(seq) < MIN_SEQUENCE_SIZE {
			getAndIncrementFromSeqmap(sequenceMap, sequenceFromTokenSlice(seq), tok, 1)
		} else {
			for i := len(seq) - 1; i >= MIN_SEQUENCE_SIZE; i -= 1 {
				getAndIncrementFromSeqmap(sequenceMap, sequenceFromTokenSlice(seq[i:]), tok, 1)
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
		getAndIncrementFromSeqmap(sequenceMap, sequenceFromTokenSlice(seq), INTERNED_LAST_TOKEN, 1)
		seq = seq[1:]
	}

	if outToks != nil {
		*outToks = append([]Token{}, tokens[1:len(tokens)-1]...)
	}
}

func TokenizeString(text string, tokens []Token, includeFirstAndLastToken bool) []Token {
	if includeFirstAndLastToken {
		Append2(&tokens, INTERNED_FIRST_TOKEN)
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
				Append2(&tokens, internString(text[head:][:end+2]))
				head += end + 2
				continue
			}
		}

		if strings.HasPrefix(subtext, "```") {
			end := strings.Index(text[head+3:], "```")
			if end != -1 {
				Append2(&tokens, internString(text[head:][:end+6]))
				head += end + 6
				continue
			}
		}

		if punct := startsWithPunctuation(text[head:]); punct != "" {
			Append2(&tokens, internString(punct))
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
		Append2(&tokens, internString(text[start:head]))
	}
	if includeFirstAndLastToken {
		Append2(&tokens, INTERNED_LAST_TOKEN)
	}
	return tokens
}

func StringFromTokens(toks []Token) string {
	result := ""
	shouldAddSpace := false
	punctBothSet := map[string]bool{}

	for _, tok := range toks {
		if tok == INTERNED_FIRST_TOKEN || tok == INTERNED_LAST_TOKEN {
			continue
		}

		strtok := internedStrings[tok]
		if slices.Contains(PUNCTUATIONS[:], strtok) {
			if slices.Contains(PUNCT_BINDING_RIGHT[:], strtok) {
				if shouldAddSpace {
					result += " "
					shouldAddSpace = false
				}
				result += strtok
			} else if slices.Contains(PUNCT_BINDING_BOTH[:], strtok) {
				if punctBothSet[strtok] {
					result += strtok
					shouldAddSpace = true
					punctBothSet[strtok] = false
				} else {
					if shouldAddSpace {
						result += " "
						shouldAddSpace = false
					}
					result += strtok
					punctBothSet[strtok] = true
				}
			} else {
				result += strtok
				shouldAddSpace = true
			}
		} else {
			if shouldAddSpace {
				result += " "
			}
			shouldAddSpace = true
			result += strtok
		}
	}

	return result
}

func GenerateTokensFromMessages(seqmap SequenceMap, msgs [][]Token, temp float64, beginning []Token) ([]Token, []int) {
	maxMessageCount := 2 + rand.IntN(1)
	alreadySeenMessages := make([]int, 0, maxMessageCount+1)

	var toks []Token
	if len(beginning) > 0 {
		toks = make([]Token, 1+len(beginning))
		toks[0] = INTERNED_FIRST_TOKEN
		for i, tok := range beginning {
			toks[1+i] = tok
		}
	} else {
		maxMessageCount -= 1
		index := rand.IntN(len(msgs))
		msg := msgs[index]
		alreadySeenMessages = append(alreadySeenMessages, index)
		split := findBestSplitPoint2(seqmap, msg)
		if split != -1 {
			msg = msg[:split]
		}
		toks = append([]Token{INTERNED_FIRST_TOKEN}, msg...)
	}

	// generate tokens
	for toks[len(toks)-1] != INTERNED_LAST_TOKEN {
		tail := toks[max(len(toks)-SEQUENCE_SIZE, 0):]

		type MessageIndexPair struct {
			Index   int
			Message []Token
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

		splitted := false
		if maxMessageCount > 1 {
			maxMessageCount -= 1

			splitPoint := findBestSplitPoint2(seqmap, msg)
			if splitPoint != -1 {
				msg = msg[:splitPoint]
				splitted = true
			}
		}

		toks = append(toks, msg...)
		if !splitted {
			toks = append(toks, INTERNED_LAST_TOKEN)
		}

		if len(toks) > 50 {
			break
		}
	}

	return toks, alreadySeenMessages
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

func findBestSplitPoint2(seqmap SequenceMap, toks []Token) int {
	bestSplitIndex := -1
	bestScore := uint(0)
	for i := len(toks) - 1; i >= 0; i -= 1 {
		tail := toks[max(i-SEQUENCE_SIZE, 0):i]
		for ; len(tail) > 0; tail = tail[1:] {
			key := sequenceFromTokenSlice(tail)
			if sequence, ok := seqmap[key]; ok {
				score := uint(0)

				if len(sequence.Relations) > 1 {
					score = sequence.Total
				} else if _, ok := sequence.Relations[INTERNED_LAST_TOKEN]; !ok {
					score = sequence.Total
				}

				if rel, ok := sequence.Relations[INTERNED_LAST_TOKEN]; ok && uint(rel) > sequence.Total/2 {
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

func getAndIncrementFromSeqmap(seqmap *SequenceMap, seq [SEQUENCE_SIZE]Token, tok Token, amount uint32) {
	sequenceMap := *seqmap

	sequence, ok := sequenceMap[seq]
	if !ok {
		sequence = WordRelations{Total: 0, Relations: make(map[Token]uint32)}
	}

	sequence.Total += uint(amount)
	sequence.Relations[tok] += amount

	sequenceMap[seq] = sequence
	*seqmap = sequenceMap
}

func startsWithPunctuation(str string) string {
	for _, punct := range PUNCTUATIONS {
		if strings.HasPrefix(str, punct) {
			// if len(str) <= len(punct) {
			// 	return punct
			// } else if isWhitespace(str[len(punct)]) != slices.Contains(PUNCT_BINDING_RIGHT[:], punct) {
			// 	return punct
			// }
			return punct
		}
	}
	return ""
}

func sequenceFromTokenSlice(slice []Token) [SEQUENCE_SIZE]Token {
	if len(slice) > 3 {
		slice = slice[len(slice)-3:]
	}

	result := [SEQUENCE_SIZE]Token{}
	for i := 0; i < len(slice); i += 1 {
		result[i] = slice[i]
	}
	return result
}

func randomIntTempered(from int, to int, temp float64) int {
	v := math.Pow(rand.Float64(), temp)
	return from + int(v*float64(to-from))
}

// func randomUintTempered(from uint, to uint, temp float64) uint {
// 	v := math.Pow(rand.Float64(), temp)
// 	return from + uint(v*float64(to-from))
// }

func isWhitespace(ch uint8) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func GenerateTokensFromSequenceMap2(seqmap SequenceMap, messages [][]Token, temp float64, beginning []Token, softLimit int) ([]Token, []int) {
	result := []Token{INTERNED_FIRST_TOKEN}
	messageCount := 1 + rand.IntN(2)
	alreadySeenMessages := []int{}

	if beginning != nil {
		result = append(result, beginning...)
	}

	for result[len(result)-1] != INTERNED_LAST_TOKEN {
		tail := result[max(len(result)-SEQUENCE_SIZE, 0):]
		foundRelations := []WordRelations{}
		allowLastToken := false

		for len(tail) > 0 {
			key := sequenceFromTokenSlice(tail)
			if relation, ok := seqmap[key]; ok {
				foundRelations = append(foundRelations, relation)
				if _, ok := relation.Relations[INTERNED_LAST_TOKEN]; ok {
					allowLastToken = true
				}
			}
			tail = tail[1:]
		}

		if len(foundRelations) == 0 {
			break
		}
		if softLimit != 0 && len(result)-1 >= softLimit && allowLastToken {
			result = append(result, INTERNED_LAST_TOKEN)
			break
		}

		relations := foundRelations[randomIntTempered(0, len(foundRelations), temp)]
		// relations := foundRelations[0]
		type TokenAmountPair struct {
			Token  Token
			Amount uint32
		}
		orderedRelations := make([]TokenAmountPair, 0, len(relations.Relations))
		total := uint(0)
		for tok, amount := range relations.Relations {
			if tok == INTERNED_LAST_TOKEN && len(result)-1 < softLimit/2 && len(orderedRelations) > 1 {
				continue
			}
			orderedRelations = append(orderedRelations, TokenAmountPair{Token: tok, Amount: amount})
			total += uint(amount)
		}
		slices.SortFunc(orderedRelations, func(a TokenAmountPair, b TokenAmountPair) int {
			if a.Amount > b.Amount {
				return -1
			}
			if a.Amount < b.Amount {
				return 1
			}
			return 0
		})

		mean := total / uint(len(orderedRelations))
		maxIndex := 0
		for i, v := range orderedRelations {
			maxIndex = i
			if uint(v.Amount) < mean {
				break
			}
		}

		relIndex := randomIntTempered(0, maxIndex, temp)
		wantedNextToken := orderedRelations[relIndex].Token
		if wantedNextToken == INTERNED_LAST_TOKEN {
			result = append(result, INTERNED_LAST_TOKEN)
			break
		}

		filtered := []int{}
		for msgIndex, msg := range messages {
			if slices.Contains(alreadySeenMessages, msgIndex) {
				continue
			}
			if slices.Contains(msg, wantedNextToken) {
				filtered = append(filtered, msgIndex)
			}
		}

		msgIndex := filtered[rand.IntN(len(filtered))]
		msg := messages[msgIndex]
		alreadySeenMessages = append(alreadySeenMessages, msgIndex)
		splited := msg[slices.Index(msg, wantedNextToken):]
		if messageCount > 1 {
			messageCount -= 1
			if len(splited) > 1 {
				splited = splited[:1+rand.IntN(len(splited)-1)]
			}
			result = append(result, splited...)
		} else {
			result = append(result, splited...)
			result = append(result, INTERNED_LAST_TOKEN)
		}
	}

	return result, alreadySeenMessages
}
