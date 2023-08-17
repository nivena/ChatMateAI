package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "strconv"
)

const apiEndpoint = "https://api.openai.com/v1/chat/completions"
const temperature = 0.5
const aiModel = "gpt-3.5-turbo-0301"

type request struct {
    Model       string    `json:"model"`
    Messages    []message `json:"messages"`
    Temperature float32   `json:"temperature"`
}

type message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type response struct {
    ID      string `json:"id"`
    Object  string `json:"object"`
    Created int    `json:"created"`
    Choices []struct {
        Index        int     `json:"index"`
        Message      message `json:"message"`
        FinishReason string  `json:"finish_reason"`
    } `json:"choices"`
    Usage struct {
        PromptTokens     int `json:"prompt_tokens"`
        CompletionTokens int `json:"completion_tokens"`
        TotalTokens      int `json:"total_tokens"`
    } `json:"usage"`
}

func OpenAI_SendMsg(msg, language string) (string){
    apiKey := os.Getenv("OPENAI_API_KEY")
    if apiKey == "" {
        Log.Errorf("Please set OPENAI_API_KEY environment variable")
    }

    client := &http.Client{}
    escapedInput := strconv.Quote(msg)
    translateText := "Translate to "+ language + ":" + escapedInput
    Log.Infof(msg)
    response, err := getAIResponse(apiKey, client, translateText)
    if err != nil {
        Log.Errorf("Error getting AI response: %s\n", err.Error())
        return err.Error()
    }

    if len(response.Choices) == 0 {
        Log.Errorf("Error: empty response")
        return "Error: Empty Response"
    }

    message := response.Choices[0].Message
    if message.Content != "" {
        Log.Infof(message.Content)
    }
    return message.Content
}
func getAIResponse(apiKey string, client *http.Client, escapedInput string) (*response, error) {
    payload := request{
        Model:       aiModel,
        Messages:    []message{{Role: "user", Content: escapedInput}},
        Temperature: temperature,
    }

    jsonPayload, err := json.Marshal(payload)
    if err != nil {
        return nil, fmt.Errorf("error marshalling payload: %s", err.Error())
    }

    req, err := http.NewRequest("POST", apiEndpoint, bytes.NewBuffer(jsonPayload))
    if err != nil {
        return nil, fmt.Errorf("error creating request: %s", err.Error())
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("error sending request: %s", err.Error())
    }

    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
    }

    var responseObj response
    err = json.NewDecoder(resp.Body).Decode(&responseObj)
    if err != nil {
        return nil, fmt.Errorf("error decoding response: %s", err.Error())
    }

    return &responseObj, nil
}

