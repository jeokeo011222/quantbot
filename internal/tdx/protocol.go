package tdx

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"strings"
)

// TDX 协议命令码
const (
	CmdLogin                  byte = 0x01
	CmdLoginResp              byte = 0x41
	CmdHeartbeat              byte = 0x02
	CmdHeartbeatResp          byte = 0x42
	CmdGetSecurityBars        byte = 0x13
	CmdGetSecurityBarsResp    byte = 0x14
	CmdGetMinuteBars          byte = 0x17
	CmdGetMinuteBarsResp      byte = 0x18
	CmdGetSecurityQuotes      byte = 0x19
	CmdGetSecurityQuotesResp  byte = 0x1A
	CmdGetTransactionData     byte = 0x1B
	CmdGetTransactionDataResp byte = 0x1C
	CmdGetSecurityInfo        byte = 0x23
	CmdGetSecurityInfoResp    byte = 0x24
	CmdGetSecurityCount       byte = 0x26
	CmdGetSecurityCountResp   byte = 0x27
	CmdGetSecurityList        byte = 0x28
	CmdGetSecurityListResp    byte = 0x29
	CmdGetMarketStat          byte = 0x2B
	CmdGetMarketStatResp      byte = 0x2C
)

// TDXFrame TDX 协议帧结构
type TDXFrame struct {
	DataType byte
	Cmd      byte
	SubCmd   byte
	BodyLen  uint16
	SeqNum   uint32
	Checksum uint16
	Data     []byte
}

// EncodeFrame 编码 TDX 帧 (LittleEndian 字节序)
// TDX 帧格式: [type(1)][cmd(1)][subcmd(1)][bodyLen(2) LE][seqNum(4) LE][body(N)][checksum(2) LE]
func EncodeFrame(cmd, subCmd byte, data []byte, seqNum uint32, encryptKey []byte, isLogin bool) []byte {
	bodyLen := len(data)
	frame := make([]byte, 11+bodyLen)

	frame[0] = cmd
	frame[1] = subCmd
	binary.LittleEndian.PutUint16(frame[2:4], uint16(bodyLen))
	binary.LittleEndian.PutUint32(frame[4:8], seqNum)

	// 数据处理
	if !isLogin && encryptKey != nil && len(encryptKey) > 0 {
		encrypted := xorEncrypt(data, encryptKey)
		copy(frame[8:], encrypted)
	} else {
		copy(frame[8:], data)
	}

	// 计算校验和
	checksum := calcChecksum(frame[0 : 8+bodyLen])
	binary.LittleEndian.PutUint16(frame[8+bodyLen:10+bodyLen], checksum)

	// 重新排列: 加上类型前缀
	finalFrame := make([]byte, 1+len(frame))
	if isLogin {
		finalFrame[0] = 0x00
	} else {
		finalFrame[0] = 0x01
	}
	copy(finalFrame[1:], frame)

	return finalFrame
}

// DecodeFrame 解码 TDX 帧
func DecodeFrame(data []byte, encryptKey []byte) (*TDXFrame, error) {
	if len(data) < 11 {
		return nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}

	frame := &TDXFrame{
		DataType: data[0],
		Cmd:      data[1],
		SubCmd:   data[2],
		BodyLen:  binary.LittleEndian.Uint16(data[3:5]),
		SeqNum:   binary.LittleEndian.Uint32(data[5:9]),
	}

	if 9+int(frame.BodyLen)+2 > len(data) {
		return nil, fmt.Errorf("frame checksum position out of bounds")
	}
	frame.Checksum = binary.LittleEndian.Uint16(data[9+frame.BodyLen : 11+frame.BodyLen])

	if frame.BodyLen > 0 && int(frame.BodyLen) <= len(data)-11 {
		frame.Data = make([]byte, frame.BodyLen)
		if encryptKey != nil && frame.DataType != 0x00 {
			decrypted := xorDecrypt(data[9:9+frame.BodyLen], encryptKey)
			copy(frame.Data, decrypted)
		} else {
			copy(frame.Data, data[9:9+frame.BodyLen])
		}
	}

	return frame, nil
}

// calcChecksum 计算帧校验和
func calcChecksum(data []byte) uint16 {
	var sum uint16
	for _, b := range data {
		sum += uint16(b)
	}
	return sum
}

// xorEncrypt XOR 加密
func xorEncrypt(data, key []byte) []byte {
	result := make([]byte, len(data))
	keyLen := len(key)
	for i, b := range data {
		result[i] = b ^ key[i%keyLen]
	}
	return result
}

// xorDecrypt XOR 解密（与加密相同）
func xorDecrypt(data, key []byte) []byte {
	return xorEncrypt(data, key)
}

// generateLoginKey 生成登录密钥
func generateLoginKey(seed string) []byte {
	hash := md5.Sum([]byte(seed))
	return hash[:]
}

// TDX 市场代码编码
const (
	MarketSH uint8 = 1 // 上海
	MarketSZ uint8 = 0 // 深圳
	MarketBJ uint8 = 2 // 北京/新三板
)

// EncodeSecurity 编码证券代码 - 6字节格式: 市场(1) + 代码ASCII(6)
func EncodeSecurity(market uint8, code string) ([]byte, error) {
	if len(code) != 6 {
		return nil, fmt.Errorf("invalid code length: %d (expected 6)", len(code))
	}

	// 检查代码是否全为数字
	for _, c := range code {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("invalid code: %s (non-numeric)", code)
		}
	}

	// 格式: 市场(1字节) + 6位ASCII代码(6字节) = 7字节
	// 但TDX实际只使用6字节代码，市场信息通过其他方式传递
	encoded := make([]byte, 6)
	for i, c := range code {
		encoded[i] = byte(c)
	}

	return encoded, nil
}

// DecodeSecurity 解码证券代码
func DecodeSecurity(data []byte) (market uint8, code string) {
	if len(data) < 6 {
		return 0, ""
	}
	// 尝试解析为ASCII代码
	var sb strings.Builder
	for i := 0; i < 6 && i < len(data); i++ {
		if data[i] >= '0' && data[i] <= '9' {
			sb.WriteByte(data[i])
		}
	}
	code = sb.String()
	if len(code) != 6 {
		// fallback: 压缩格式解码
		codeInt := 0
		for i := 0; i < len(data) && i < 4; i++ {
			codeInt = codeInt*256 + int(data[i])
		}
		code = fmt.Sprintf("%06d", codeInt)
	}
	return
}

// 请求体构造辅助函数

// BuildKlineRequest 构造K线请求体
func BuildKlineRequest(market, code string, category uint16, startPos, count uint16) ([]byte, error) {
	mkt := marketToByte(market)
	codeBytes, err := EncodeSecurity(mkt, code)
	if err != nil {
		return nil, err
	}

	// K线请求体: 市场(1) + 代码(6) + 周期(2 LE) + 起始(2 LE) + 数量(2 LE) = 13字节
	body := make([]byte, 1+6+2+2+2)
	body[0] = mkt
	copy(body[1:], codeBytes)
	binary.LittleEndian.PutUint16(body[7:], category)
	binary.LittleEndian.PutUint16(body[9:], startPos)
	binary.LittleEndian.PutUint16(body[11:], count)

	return body, nil
}

// BuildMinuteBarsRequest 构造分时请求体
func BuildMinuteBarsRequest(market, code string, startPos, count uint16) ([]byte, error) {
	mkt := marketToByte(market)
	codeBytes, err := EncodeSecurity(mkt, code)
	if err != nil {
		return nil, err
	}

	// 分时请求体: 市场(1) + 代码(6) + 起始(2 LE) + 数量(2 LE) = 11字节
	body := make([]byte, 1+6+2+2)
	body[0] = mkt
	copy(body[1:], codeBytes)
	binary.LittleEndian.PutUint16(body[7:], startPos)
	binary.LittleEndian.PutUint16(body[9:], count)

	return body, nil
}

// BuildQuotesRequest 构造行情请求体
func BuildQuotesRequest(market string, codes []string) ([]byte, error) {
	mkt := marketToByte(market)

	// 行情请求体: 市场(1) + 数量(2 LE) + [代码(6)]*N
	body := make([]byte, 0)
	body = append(body, mkt)
	// 数量 (LittleEndian 2字节)
	countBuf := make([]byte, 2)
	binary.LittleEndian.PutUint16(countBuf, uint16(len(codes)))
	body = append(body, countBuf...)

	for _, code := range codes {
		codeBytes, err := EncodeSecurity(mkt, code)
		if err != nil {
			return nil, err
		}
		body = append(body, codeBytes...)
	}

	return body, nil
}

// BuildTransactionRequest 构造逐笔成交请求体
func BuildTransactionRequest(market, code string, startPos, count uint16) ([]byte, error) {
	return BuildMinuteBarsRequest(market, code, startPos, count)
}

// BuildSecurityListRequest 构造证券列表请求体
func BuildSecurityListRequest(market uint8, startPos, count uint16) []byte {
	body := make([]byte, 5)
	body[0] = market
	binary.LittleEndian.PutUint16(body[1:], startPos)
	binary.LittleEndian.PutUint16(body[3:], count)
	return body
}

// BuildSecurityCountRequest 构造证券数量请求体
func BuildSecurityCountRequest(market uint8) []byte {
	return []byte{market}
}

// BuildSecurityInfoRequest 构造证券信息请求体
func BuildSecurityInfoRequest(market, code string) ([]byte, error) {
	mkt := marketToByte(market)
	codeBytes, err := EncodeSecurity(mkt, code)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 1+6)
	body[0] = mkt
	copy(body[1:], codeBytes)
	return body, nil
}

// BuildMarketStatRequest 构造市场统计请求体
func BuildMarketStatRequest(market uint8) []byte {
	return []byte{market}
}

func marketToByte(market string) uint8 {
	switch strings.ToLower(market) {
	case "sh", "sse", "1":
		return MarketSH
	case "sz", "szse", "0":
		return MarketSZ
	case "bj", "bse", "2":
		return MarketBJ
	default:
		return MarketSH
	}
}

// K线类型编码
const (
	KlineCategoryDay   uint16 = 0x000A
	KlineCategoryWeek  uint16 = 0x000B
	KlineCategoryMonth uint16 = 0x000C
	KlineCategoryMin1  uint16 = 0x0000
	KlineCategoryMin5  uint16 = 0x0001
	KlineCategoryMin15 uint16 = 0x0002
	KlineCategoryMin30 uint16 = 0x0003
	KlineCategoryMin60 uint16 = 0x0004
)

// ParseKlineCategory 解析K线类型字符串
func ParseKlineCategory(period string) (uint16, error) {
	switch strings.ToLower(period) {
	case "day":
		return KlineCategoryDay, nil
	case "week":
		return KlineCategoryWeek, nil
	case "month":
		return KlineCategoryMonth, nil
	case "1min":
		return KlineCategoryMin1, nil
	case "5min":
		return KlineCategoryMin5, nil
	case "15min":
		return KlineCategoryMin15, nil
	case "30min":
		return KlineCategoryMin30, nil
	case "60min":
		return KlineCategoryMin60, nil
	default:
		return 0, fmt.Errorf("unknown period: %s", period)
	}
}
