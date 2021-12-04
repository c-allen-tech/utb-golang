package youtube

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
)

func (c *Client) decipherURL(ctx context.Context, videoID string, cipher string) (string, error) {
	params, err := url.ParseQuery(cipher)
	if err != nil {
		return "", err
	}

	uri, err := url.Parse(params.Get("url"))
	if err != nil {
		return "", err
	}

	config, err := c.getPlayerConfig(ctx, videoID)
	if err != nil {
		return "", err
	}

	// decrypt s-parameter
	bs, err := config.decrypt([]byte(params.Get("s")))
	if err != nil {
		return "", err
	}

	query := uri.Query()
	query.Add(params.Get("sp"), string(bs))
	uri.RawQuery = query.Encode()

	return uri.String(), nil
}

const (
	jsvarStr   = "[a-zA-Z_\\$][a-zA-Z_0-9]*"
	reverseStr = ":function\\(a\\)\\{" +
		"(?:return )?a\\.reverse\\(\\)" +
		"\\}"
	spliceStr = ":function\\(a,b\\)\\{" +
		"a\\.splice\\(0,b\\)" +
		"\\}"
	swapStr = ":function\\(a,b\\)\\{" +
		"var c=a\\[0\\];a\\[0\\]=a\\[b(?:%a\\.length)?\\];a\\[b(?:%a\\.length)?\\]=c(?:;return a)?" +
		"\\}"
)

var (
	actionsObjRegexp = regexp.MustCompile(fmt.Sprintf(
		"var (%s)=\\{((?:(?:%s%s|%s%s|%s%s),?\\n?)+)\\};", jsvarStr, jsvarStr, swapStr, jsvarStr, spliceStr, jsvarStr, reverseStr))

	actionsFuncRegexp = regexp.MustCompile(fmt.Sprintf(
		"function(?: %s)?\\(a\\)\\{"+
			"a=a\\.split\\(\"\"\\);\\s*"+
			"((?:(?:a=)?%s\\.%s\\(a,\\d+\\);)+)"+
			"return a\\.join\\(\"\"\\)"+
			"\\}", jsvarStr, jsvarStr, jsvarStr))

	reverseRegexp = regexp.MustCompile(fmt.Sprintf("(?m)(?:^|,)(%s)%s", jsvarStr, reverseStr))
	spliceRegexp  = regexp.MustCompile(fmt.Sprintf("(?m)(?:^|,)(%s)%s", jsvarStr, spliceStr))
	swapRegexp    = regexp.MustCompile(fmt.Sprintf("(?m)(?:^|,)(%s)%s", jsvarStr, swapStr))
)

func (config playerConfig) decrypt(cyphertext []byte) ([]byte, error) {
	operations, err := config.parseDecipherOps()
	if err != nil {
		return nil, err
	}

	// apply operations
	bs := []byte(cyphertext)
	for _, op := range operations {
		bs = op(bs)
	}

	return bs, nil
}

/*
	parses decipher operations from https://youtube.com/s/player/4fbb4d5b/player_ias.vflset/en_US/base.js

	var Mt={
	splice:function(a,b){a.splice(0,b)},
	reverse:function(a){a.reverse()},
	EQ:function(a,b){var c=a[0];a[0]=a[b%a.length];a[b%a.length]=c}};

	a=a.split("");
	Mt.splice(a,3);
	Mt.EQ(a,39);
	Mt.splice(a,2);
	Mt.EQ(a,1);
	Mt.splice(a,1);
	Mt.EQ(a,35);
	Mt.EQ(a,51);
	Mt.splice(a,2);
	Mt.reverse(a,52);
	return a.join("")
*/
func (config playerConfig) parseDecipherOps() (operations []DecipherOperation, err error) {
	objResult := actionsObjRegexp.FindSubmatch(config)
	funcResult := actionsFuncRegexp.FindSubmatch(config)
	if len(objResult) < 3 || len(funcResult) < 2 {
		return nil, fmt.Errorf("error parsing signature tokens (#obj=%d, #func=%d)", len(objResult), len(funcResult))
	}

	obj := objResult[1]
	objBody := objResult[2]
	funcBody := funcResult[1]

	var reverseKey, spliceKey, swapKey string

	if result := reverseRegexp.FindSubmatch(objBody); len(result) > 1 {
		reverseKey = string(result[1])
	}
	if result := spliceRegexp.FindSubmatch(objBody); len(result) > 1 {
		spliceKey = string(result[1])
	}
	if result := swapRegexp.FindSubmatch(objBody); len(result) > 1 {
		swapKey = string(result[1])
	}

	regex, err := regexp.Compile(fmt.Sprintf("(?:a=)?%s\\.(%s|%s|%s)\\(a,(\\d+)\\)", obj, reverseKey, spliceKey, swapKey))
	if err != nil {
		return nil, err
	}

	var ops []DecipherOperation
	for _, s := range regex.FindAllSubmatch(funcBody, -1) {
		switch string(s[1]) {
		case reverseKey:
			ops = append(ops, reverseFunc)
		case swapKey:
			arg, _ := strconv.Atoi(string(s[2]))
			ops = append(ops, newSwapFunc(arg))
		case spliceKey:
			arg, _ := strconv.Atoi(string(s[2]))
			ops = append(ops, newSpliceFunc(arg))
		}
	}
	return ops, nil
}

func (c *Client) getPlayerConfig(ctx context.Context, videoID string) (playerConfig, error) {
	if config := c.decipherOpsCache.Get(videoID); config != nil {
		return config, nil
	}

	config, err := c.fetchPlayerConfig(ctx, videoID)
	if err != nil {
		return nil, err
	}

	c.decipherOpsCache.Set(videoID, config)
	return config, err
}
