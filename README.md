# README

## About

This is the official Wails Vanilla template.

You can configure the project by editing `wails.json`. More information about the project settings can be found
here: https://wails.io/docs/reference/project-config

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development
server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser
and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect
to this in your browser, and you can call your Go code from devtools.

## Building

To build a redistributable, production mode package, use `wails build`.

## How to Build a Wails app

1. Start by installing the Wails for Linux/Windows/Mac
2. wails init -n ChatMateAI
3. wails build
4. go get go.mau.fi/whatsmeow (install external package for whatsapp)
5. Add your own openai.go to interact with chatgpt
6. Important to have chatGPT - key stored as before interacting
   export OPENAI_API_KEY=<your API key> or store it in bashrc

//send <phone num> "some message"

// for group identify the group ID using listgroups and then
// send <groupJID@g.us> "some message"
