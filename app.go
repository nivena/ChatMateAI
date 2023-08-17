package main

import (
	"context"
	"fmt"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	//"golang.org/x/text/language"
)

// App struct
type App struct {
    ctx context.Context
}
//var args = []string{}
var sendargs = make([]string, 2)
var rcvargs = make([]string, 1)
var translateTo string
var runtimectx context.Context

// NewApp creates new App application struct
func NewApp() *App {
    return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
    a.ctx = ctx
    runtimectx = ctx
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
    return fmt.Sprintf("Hello %s, It's show time!", name)
}

func (a *App) SendNumber(num string) bool{
    //runtime.WindowExecJS(a.ctx, `document.getElementById("number").value="thhis is my cpu;"`)

    fmt.Printf("This is our number %s!\n",num)
    sendargs[0] = num
    return true
}


func (a *App) SendMsg(msg string) bool{
    fmt.Printf("Send Msg: %s  %s\n", msg, sendargs)
    s := OpenAI_SendMsg(msg, "english")
    s = s[1 : len(s)-1]
    sendargs[1] = s
    fmt.Printf("Translated to english from AI: %s\n", sendargs[1] )
    go handleCmd("send", sendargs) // send the msg
    return true
}

func ReceiveMsg(msg string){
    fmt.Println("rxd msg:", msg, translateTo)
    s := OpenAI_SendMsg(msg[13:], translateTo) // 13 offset because keyword conversation: must be ignored
    s = s[1: len(s)-1]
    fmt.Println("s value:", s)
    rcvargs[0] = s
    fmt.Printf("Re-translated to %s from AI: %s\n", translateTo, rcvargs[0] )
    runtime.EventsEmit(runtimectx, "rxmsg", rcvargs[0])
}

func (a *App) InitWhatsApp(language string) {
    fmt.Println("start whatsapp:")
    translateTo = language
    whatsappStart()
}

// This function serves as an entry point for adding custom message processing logic.
// You can extend this function to handle additional message-related operations.
func placeholderComment() {
    // This function is intentionally left empty. It can be extended with custom logic
    // to handle message processing or any related functionality in the future.
}







