package engineer

import (
	"strconv"
	"strings"
)

// The numbers a corner may be called by, as words, in every language the
// coach writes in. The lines are spoken, so the coach is asked to write
// numbers as words, and it does: "Curva uno". A rule that only knew digits
// would not see the corner named, and would name it again in front. Every
// language also takes the English words, because a model writing Spanish
// still slips into them.

// numberWords is, per language, each spelling of one to thirty and the number
// it is. Alternative spellings — with and without accents, feminine forms,
// hyphens or spaces — are all here, because a speech engine reads any of them
// and a driver hears the same corner.
var numberWords = map[string]map[string]int{
	"en": words(
		"one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen "+
			"seventeen eighteen nineteen twenty",
		"twenty-one twenty-two twenty-three twenty-four twenty-five twenty-six twenty-seven twenty-eight twenty-nine thirty",
		"twenty one=21|twenty two=22|twenty three=23|twenty four=24|twenty five=25|twenty six=26|"+
			"twenty seven=27|twenty eight=28|twenty nine=29",
	),
	"es": words(
		"uno dos tres cuatro cinco seis siete ocho nueve diez once doce trece catorce quince dieciséis diecisiete "+
			"dieciocho diecinueve veinte",
		"veintiuno veintidós veintitrés veinticuatro veinticinco veintiséis veintisiete veintiocho veintinueve treinta",
		"una=1|un=1|dieciseis=16|veintiuna=21|veintiún=21|veintiun=21|veintidos=22|veintitres=23|veintiseis=26",
	),
	"de": words(
		"eins zwei drei vier fünf sechs sieben acht neun zehn elf zwölf dreizehn vierzehn fünfzehn sechzehn "+
			"siebzehn achtzehn neunzehn zwanzig",
		"einundzwanzig zweiundzwanzig dreiundzwanzig vierundzwanzig fünfundzwanzig sechsundzwanzig "+
			"siebenundzwanzig achtundzwanzig neunundzwanzig dreißig",
		"eine=1|ein=1|fuenf=5|zwoelf=12|fuenfzehn=15|fuenfundzwanzig=25|dreissig=30",
	),
	"fr": words(
		"un deux trois quatre cinq six sept huit neuf dix onze douze treize quatorze quinze seize dix-sept "+
			"dix-huit dix-neuf vingt",
		"vingt-et-un vingt-deux vingt-trois vingt-quatre vingt-cinq vingt-six vingt-sept vingt-huit vingt-neuf trente",
		"une=1|dix sept=17|dix huit=18|dix neuf=19|vingt et un=21|vingt-un=21|vingt deux=22|vingt trois=23|"+
			"vingt quatre=24|vingt cinq=25|vingt six=26|vingt sept=27|vingt huit=28|vingt neuf=29",
	),
	"it": words(
		"uno due tre quattro cinque sei sette otto nove dieci undici dodici tredici quattordici quindici sedici "+
			"diciassette diciotto diciannove venti",
		"ventuno ventidue ventitré ventiquattro venticinque ventisei ventisette ventotto ventinove trenta",
		"una=1|ventitre=23",
	),
	"pt": words(
		"um dois três quatro cinco seis sete oito nove dez onze doze treze catorze quinze dezesseis dezessete "+
			"dezoito dezenove vinte",
		"vinte-e-um vinte-e-dois vinte-e-três vinte-e-quatro vinte-e-cinco vinte-e-seis vinte-e-sete vinte-e-oito "+
			"vinte-e-nove trinta",
		"uma=1|duas=2|tres=3|quatorze=14|dezasseis=16|dezassete=17|dezanove=19|vinte e um=21|vinte e uma=21|"+
			"vinte e dois=22|vinte e duas=22|vinte e três=23|vinte e tres=23|vinte e quatro=24|vinte e cinco=25|"+
			"vinte e seis=26|vinte e sete=27|vinte e oito=28|vinte e nove=29",
	),
	"nl": words(
		"een twee drie vier vijf zes zeven acht negen tien elf twaalf dertien veertien vijftien zestien zeventien "+
			"achttien negentien twintig",
		"eenentwintig tweeëntwintig drieëntwintig vierentwintig vijfentwintig zesentwintig zevenentwintig "+
			"achtentwintig negenentwintig dertig",
		"één=1|éénentwintig=21|tweeentwintig=22|drieentwintig=23",
	),
	"pl": words(
		"jeden dwa trzy cztery pięć sześć siedem osiem dziewięć dziesięć jedenaście dwanaście trzynaście "+
			"czternaście piętnaście szesnaście siedemnaście osiemnaście dziewiętnaście dwadzieścia",
		"dwadzieścia-jeden dwadzieścia-dwa dwadzieścia-trzy dwadzieścia-cztery dwadzieścia-pięć dwadzieścia-sześć "+
			"dwadzieścia-siedem dwadzieścia-osiem dwadzieścia-dziewięć trzydzieści",
		"jedna=1|jedno=1|dwie=2|piec=5|szesc=6|dziewiec=9|dziesiec=10|jedenascie=11|dwanascie=12|trzynascie=13|"+
			"czternascie=14|pietnascie=15|szesnascie=16|siedemnascie=17|osiemnascie=18|dziewietnascie=19|"+
			"dwadziescia=20|dwadzieścia jeden=21|dwadzieścia dwa=22|dwadzieścia trzy=23|dwadzieścia cztery=24|"+
			"dwadzieścia pięć=25|dwadzieścia sześć=26|dwadzieścia siedem=27|dwadzieścia osiem=28|"+
			"dwadzieścia dziewięć=29|trzydziesci=30",
	),
}

// words builds one language's table from the plain spellings of one to
// twenty and twenty-one to thirty, in order and space-separated, and extras
// written "spelling=number", separated by bars.
func words(oneToTwenty, twentyOneToThirty, extras string) map[string]int {
	out := make(map[string]int, 64)
	for i, w := range strings.Fields(oneToTwenty) {
		out[w] = i + 1
	}
	for i, w := range strings.Fields(twentyOneToThirty) {
		out[w] = i + 21
	}
	for _, e := range strings.Split(extras, "|") {
		spelling, n, ok := strings.Cut(e, "=")
		v, err := strconv.Atoi(n)
		if !ok || err != nil || v <= 0 || v > 30 {
			panic("engineer: bad number word " + e)
		}
		out[spelling] = v
	}
	return out
}
