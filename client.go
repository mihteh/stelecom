package stelecom

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const header = "GLAVFINANS"

const SessionIdExpired = 4
const DirectionIsClosed = 5

const SmsStatusDelivered = "delivered"

type Sms struct {
	Phone string
	Text  string
}

type StreamTelecomer interface {
	GetBalance() (float64, error)
	SendSms(Sms) responseFromSendSms
	Authorize(login string, password string) (string, error)
	GetSmsStatus(smsId string) (string, error)
	GetSessionId() string
}

func NewClient(basePath string, timeout time.Duration) StreamTelecomer {
	return &stClient{
		basePath: basePath,
		timeout:  timeout,
	}
}

func NewDefaultClient() StreamTelecomer {
	return NewClient("http://gateway.api.sc/rest", 10*time.Second)
}

type stClient struct {
	timeout   time.Duration
	basePath  string
	login     string
	password  string
	sessionId string
	header    string
}

func getValidSmsForSend() Sms {
	sms := Sms{
		Phone: "71234567890",
		Text:  `Тестовая SMS`,
	}
	return sms
}

func (c stClient) GetSessionId() string {
	return c.sessionId
}

func (c stClient) GetBalance() (float64, error) {
	if c.sessionId == "" {
		return -1, errors.New("Клиент не авторизован. Требуется вызвать метод Authorize с корректными данными.")
	}

	urlVals := url.Values{"sessionId": {c.sessionId}}

	httpClient := http.Client{Timeout: c.timeout}
	resp, err := httpClient.Get(c.basePath + "/Balance?" + urlVals.Encode())
	if err != nil {
		return -1, err
	}

	data, err := ioutil.ReadAll(io.LimitReader(resp.Body, 2048))
	defer resp.Body.Close()

	if err != nil {
		return -1, err
	}

	var value float64
	if err = json.Unmarshal(data, &value); err != nil {
		return -1, err
	}
	return value, nil
}

// ответ от StreamTelecom если ошибка
type sTelecomErrorResponse struct {
	Code int    `json:"code"`
	Desc string `json:"desc"`
}

type responseFromSendSms struct {
	HttpStatusCode int                   // сюда передаём код ответа что бы отличить ошибку метода от ошибки в StreamTelecom
	SmsIds         []string              // массив id sms при успехе
	ResponseError  sTelecomErrorResponse // код и описание ошибки из StreamTelecom
	Error          error                 // ошибка
}

func (c stClient) SendSms(sms Sms) responseFromSendSms {
	urlVals := url.Values{
		"sessionId":          {c.sessionId},
		"destinationAddress": {sms.Phone},
		"data":               {sms.Text},
		"validity":           {"1440"},
		"sourceAddress":      {c.header},
	}

	httpClient := http.Client{Timeout: c.timeout}

	form := bytes.NewReader([]byte(urlVals.Encode()))

	resp, err := httpClient.Post(fmt.Sprintf("%s/Send/SendSms/", c.basePath),
		"application/x-www-form-urlencoded", form)
	if err != nil {
		return responseFromSendSms{
			http.StatusInternalServerError,
			[]string{},
			sTelecomErrorResponse{},
			err,
		}
	}

	data, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		return responseFromSendSms{
			http.StatusInternalServerError,
			[]string{},
			sTelecomErrorResponse{},
			err,
		}
	}

	var errResp sTelecomErrorResponse
	if resp.StatusCode != http.StatusOK {
		if err = json.Unmarshal(data, &errResp); err != nil {
			return responseFromSendSms{
				http.StatusInternalServerError,
				[]string{},
				sTelecomErrorResponse{},
				err,
			}
		}

		if errResp.Code == DirectionIsClosed {
			return responseFromSendSms{
				http.StatusPaymentRequired,
				[]string{},
				errResp,
				nil,
			}
		}

		return responseFromSendSms{
			http.StatusInternalServerError,
			[]string{},
			errResp,
			nil,
		}
	}

	var value []string
	if err = json.Unmarshal(data, &value); err != nil {
		return responseFromSendSms{
			http.StatusInternalServerError,
			[]string{},
			sTelecomErrorResponse{},
			err,
		}
	}
	return responseFromSendSms{
		http.StatusOK,
		value,
		sTelecomErrorResponse{},
		nil,
	}
}

func (c *stClient) Authorize(login string, password string) (string, error) {
	c.login = login
	c.password = password
	c.header = header
	c.sessionId = ""

	urlVals := url.Values{
		"login":    {c.login},
		"password": {c.password},
	}
	params := urlVals.Encode()

	httpClient := http.Client{Timeout: c.timeout}
	resp, err := httpClient.Get(fmt.Sprintf("%s/Session?%s", c.basePath, params))
	if err != nil {
		return "", err
	}

	data, err := ioutil.ReadAll(io.LimitReader(resp.Body, 2048))
	defer resp.Body.Close()

	if err != nil {
		return "", err
	}
	c.sessionId = strings.Replace(string(data), "\"", "", -1)

	return c.sessionId, nil
}

// Описывает ответ при успешном запросе статуса SMS
type sTelecomGetSmsStatusResponse struct {
	State            int     `json:"State"`
	ReportedDateUtc  *string `json:"ReportedDateUtc,omitempty"`  // can be null
	CreationDateUtc  *string `json:"CreationDateUtc,omitempty"`  // can be null
	SubmittedDateUtc *string `json:"SubmittedDateUtc,omitempty"` // can be null
	TimeStampUtc     *string `json:"TimeStampUtc,omitempty"`     // can be null
	StateDescription string  `json:"StateDescription"`
	Price            *string `json:"Price"` // can be null
}

func (c *stClient) GetSmsStatus(smsId string) (string, error) {
	urlVals := url.Values{
		"sessionId": {c.sessionId},
		"messageId": {smsId},
	}
	httpClient := http.Client{Timeout: c.timeout}
	params := urlVals.Encode()
	resp, err := httpClient.Get(fmt.Sprintf("%s/State?%s", c.basePath, params))
	if err != nil {
		return "", err
	}

	data, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("Ошибка, код HTTP %d: %v", resp.StatusCode, err)
	}

	var errResp sTelecomErrorResponse
	if resp.StatusCode != http.StatusOK {
		if err = json.Unmarshal(data, &errResp); err != nil {
			return "", err
		}
		return "", fmt.Errorf("Ошибка, код HTTP %d", resp.StatusCode)
	}

	var value sTelecomGetSmsStatusResponse
	if err = json.Unmarshal(data, &value); err != nil {
		return "", err
	}

	return strings.ToLower(value.StateDescription), nil
}
