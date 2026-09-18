package exchange

import (
	"fmt"
	urlpkg "net/url"
	"strings"
	"time"

	"github.com/quant1x/pkg/fastjson"
	"github.com/quant1x/x/api"
	"github.com/quant1x/x/http"
	"github.com/quant1x/x/logger"
)

const (
	urlEastMonryZhKLine = "http://push2his.eastmoney.com/api/qt/stock/kline/get"
)

var (
	// 复权
	adjustDict = map[string]string{
		"qfq": "1",
		"hfq": "2",
		"nil": "0",
	}
	// 周期
	periodDict = map[string]string{
		"daily":   "101",
		"weekly":  "102",
		"monthly": "103",
	}
)

type KLine struct {
	Date   string  `json:"date" array:"0" name:"日期" dataframe:"date,string"`
	Open   float64 `json:"open" array:"1" name:"开盘价" dataframe:"open,float64"`
	Close  float64 `json:"close" array:"2" name:"收盘价" dataframe:"close,float64"`
	High   float64 `json:"high" array:"3" name:"最高价" dataframe:"high,float64"`
	Low    float64 `json:"low" array:"4" name:"最低价" dataframe:"low,float64"`
	Volume int64   `json:"volume" array:"5" name:"成交量" dataframe:"volume,int64"`
	Amount float64 `json:"amount" array:"6" name:"成交金额" dataframe:"amount,float64"`
	//Amplitude    float64 `json:"amplitude" array:"7" name:"振幅" dataframe:"-"`
	//RiseAndFall  float64 `json:"rise_and_fall" array:"8" name:"涨跌幅" dataframe:"-"`
	//UpAndDown    float64 `json:"up_and_down" array:"9" name:"涨跌额" dataframe:"-"`
	//TurnoverRate float64 `json:"turnover_rate" array:"10" name:"换手率" dataframe:"-"`
}

// K线历史
func stock_hist(marketId int, symbol string, args ...string) ([]byte, error) {
	period := "daily"
	start_date := "19700101"
	end_date := "20500101"
	adjust := "qfq"
	argc := len(args)
	if argc >= 1 {
		start_date = args[0]
	}
	if argc >= 2 {
		end_date = args[1]
	}
	if argc >= 3 {
		adjust = args[2]
	}

	timestamp := time.Now().UnixMilli()
	params := urlpkg.Values{
		"fields1": {"f1,f2,f3,f4,f5,f6"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f116"},
		"ut":      {"7eea3edcaed734bea9cbfc24409ed989"},
		"klt":     {periodDict[period]},
		"fqt":     {adjustDict[adjust]},
		"secid":   {fmt.Sprintf("%d.%s", marketId, symbol)},
		"beg":     {start_date},
		"end":     {end_date},
		"_":       {fmt.Sprint(timestamp)},
	}
	url := urlEastMonryZhKLine + "?" + params.Encode()
	data, err := http.Get(url)
	return data, err
}

// A 下载A股数据
func A(code string) ([]KLine, error) {
	marketId, _, symbol := DetectMarket(code)
	data, err := stock_hist(int(marketId), symbol)
	var kl = []KLine{}
	obj, err := fastjson.ParseBytes(data)
	if err != nil {
		logger.Errorf("%+v\n", err)
		return kl, nil
	}
	errCode := obj.GetInt("rc")
	if errCode != 0 {
		logger.Errorf("%d: %s\n", err, obj.GetString("msg"))
		return kl, nil
	}
	_ = data
	biz := obj.Get("data")
	if biz == nil {
		logger.Errorf("数据非法\n")
		return kl, nil
	}

	history := biz.GetArray("klines")
	if history == nil {
		logger.Errorf("数据非法\n")
		return kl, nil
	}
	for _, item := range history {
		if item.Type() != fastjson.TypeString {
			continue
		}
		sb, err := item.StringBytes()
		if err != nil {
			logger.Fatalf("cannot obtain string: %s", err)
		}

		tmp := string(sb)
		hd := strings.Split(tmp, ",")
		var kl0 KLine
		err = api.Convert(hd, &kl0)
		if err == nil {
			kl = append(kl, kl0)
		}
	}
	return kl, nil
}
