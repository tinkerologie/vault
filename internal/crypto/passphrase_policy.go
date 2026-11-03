package crypto

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MinPassphraseLength is the fewest characters a new backup passphrase may
// have.
const MinPassphraseLength = 12

const minPassphraseDistinct = 6

// commonPassphraseWords are bases of the passwords tried first by guessing
// tools. A passphrase made only of these plus digits and symbols is refused.
// This list complements the length rule; it is not a breach database. Longer
// words come before words they contain, so removal takes the whole word.
var commonPassphraseWords = []string{
	"password", "passphrase", "passwort", "qwertyuiop", "qwerty", "asdfghjkl", "asdfgh",
	"zxcvbnm", "zxcvbn", "1q2w3e4r", "qazwsxedcrfv", "qazwsxedc", "qazwsx",
	"1qaz2wsx3edc", "1qaz2wsx", "zaq12wsx", "qweasdzxc", "qweasd", "asdzxc", "letmein", "welcome", "administrator", "admin",
	"root", "login", "master", "secret", "changeme", "default", "backup", "backups",
	"vault", "unraid", "server", "iloveyou", "princess", "dragon", "monkey", "football",
	"baseball", "basketball", "superman", "batman", "sunshine", "shadow", "michael",
	"jennifer", "trustno", "whatever", "freedom", "starwars", "pokemon", "spring",
	"summer", "autumn", "winter", "january", "february", "march", "april", "june",
	"july", "august", "september", "october", "november", "december", "monday",
	"tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "hunter", "test",
	"correcthorsebatterystaple",
}

var passphraseLeet = map[rune]rune{'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '@': 'a', '$': 's', '!': 'i'}

// CheckPassphraseStrength rejects passphrases that are cheap to guess offline
// against a copied backup: too short, too repetitive, a keyboard or number run,
// or nothing but common words with digits and symbols. The message is safe to
// show the user and never contains the passphrase.
func CheckPassphraseStrength(p string) error {
	if utf8.RuneCountInString(p) < MinPassphraseLength {
		return errors.New("passphrase must be at least 12 characters")
	}
	lower := strings.ToLower(p)
	distinct := map[rune]bool{}
	runes := []rune(lower)
	stepped := 0
	for i, r := range runes {
		distinct[r] = true
		if i > 0 {
			if d := r - runes[i-1]; d >= -1 && d <= 1 {
				stepped++
			}
		}
	}
	if len(distinct) < minPassphraseDistinct {
		return errors.New("passphrase is too repetitive: use more different characters")
	}
	if stepped*4 >= (len(runes)-1)*3 {
		return errors.New("passphrase is too predictable: avoid runs such as 123456 or abcdef")
	}
	if isAllDigits(lower) && len(runes) < 20 {
		return errors.New("passphrase is too predictable: a number needs at least 20 digits")
	}
	if onlyCommonWords(lower, false) || onlyCommonWords(lower, true) {
		return errors.New("passphrase is too common: use several unrelated words or a generated value")
	}
	return nil
}

func isAllDigits(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) == -1
}

// onlyCommonWords reports whether s contains a common word and fewer than four
// other letters. Digits and symbols do not count, because guessing tools append
// them to words first. With leet set, they can also spell a word (p@ssw0rd), but
// what is left over still counts by what the character originally was, so a
// suffix such as 12345! is not mistaken for letters.
func onlyCommonWords(s string, leet bool) bool {
	var norm []rune
	var wasLetter []bool
	for _, r := range s {
		mapped := r
		if leet {
			if m, ok := passphraseLeet[r]; ok {
				mapped = m
			}
		}
		if unicode.IsLetter(mapped) || unicode.IsDigit(mapped) {
			norm = append(norm, mapped)
			wasLetter = append(wasLetter, unicode.IsLetter(r))
		}
	}
	found := false
	for _, w := range commonPassphraseWords {
		word := []rune(w)
		for i := 0; i+len(word) <= len(norm); {
			if !slices.Equal(norm[i:i+len(word)], word) {
				i++
				continue
			}
			norm = slices.Delete(norm, i, i+len(word))
			wasLetter = slices.Delete(wasLetter, i, i+len(word))
			found = true
		}
	}
	if !found {
		return false
	}
	rest := 0
	for _, letter := range wasLetter {
		if letter {
			rest++
		}
	}
	return rest < 4
}
