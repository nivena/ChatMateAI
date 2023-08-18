package main

import (
    "bufio"
    "context"
    "encoding/hex"
    "encoding/json"
    "errors"
    "flag"
    "fmt"
    "mime"
    "net/http"
    "os"
    "os/signal"
    "strconv"
    "strings"
    "sync/atomic"
    "syscall"
    "time"

    _ "github.com/mattn/go-sqlite3"
    "github.com/mdp/qrterminal/v3"
    "google.golang.org/protobuf/proto"

    "go.mau.fi/whatsmeow"
    "go.mau.fi/whatsmeow/appstate"
    waBinary "go.mau.fi/whatsmeow/binary"
    waProto "go.mau.fi/whatsmeow/binary/proto"
    "go.mau.fi/whatsmeow/store"
    "go.mau.fi/whatsmeow/store/sqlstore"
    "go.mau.fi/whatsmeow/types"
    "go.mau.fi/whatsmeow/types/events"
    waLog "go.mau.fi/whatsmeow/util/log"
)

var cli *whatsmeow.Client
var Log waLog.Logger
// Additional insight for the variables below
var logLevel = "INFO" // Set the log level
var debugLogs = flag.Bool("debug", false, "Enable debug logs?") // Enable debug logs
var dbDialect = flag.String("db-dialect", "sqlite3", "Database dialect (sqlite3 or postgres)") // Specify the database dialect
var dbAddress = flag.String("db-address", "file:mdtest.db?_foreign_keys=on", "Database address") // Set the database address
var requestFullSync = flag.Bool("request-full-sync", false, "Request full (1 year) history sync when logging in?") // Specify full history sync
var pairRejectChan = make(chan bool, 1) // Channel for pair rejection

func whatsappStart() {
    waBinary.IndentXML = true
    flag.Parse()

    if *debugLogs {
        logLevel = "DEBUG"
    }
    if *requestFullSync {
        store.DeviceProps.RequireFullSync = proto.Bool(true)
    }
    Log = waLog.Stdout("Main", logLevel, true)

    dbLog := waLog.Stdout("Database", logLevel, true)
    storeContainer, err := sqlstore.New(*dbDialect, *dbAddress, dbLog)
    if err != nil {
        Log.Errorf("Failed to connect to database: %v", err)
        return
    }
    device, err := storeContainer.GetFirstDevice()
    if err != nil {
        Log.Errorf("Failed to get device: %v", err)
        return
    }

    cli = whatsmeow.NewClient(device, waLog.Stdout("Client", logLevel, true))
    var isWaitingForPair atomic.Bool
    cli.PrePairCallback = func(jid types.JID, platform, businessName string) bool {
        isWaitingForPair.Store(true)
        defer isWaitingForPair.Store(false)
        Log.Infof("Pairing %s (platform: %q, business name: %q). Type r within 3 seconds to reject pair", jid, platform, businessName)
        select {
        case reject := <-pairRejectChan:
            if reject {
                Log.Infof("Rejecting pair")
                return false
            }
        case <-time.After(3 * time.Second):
        }
        Log.Infof("Accepting pair")
        return true
    }

    ch, err := cli.GetQRChannel(context.Background())
    if err != nil {
        // This error means that we're already Log.ed in, so ignore it.
        if !errors.Is(err, whatsmeow.ErrQRStoreContainsID) {
            Log.Errorf("Failed to get QR channel: %v", err)
        }
    } else {
        go func() {
            for evt := range ch {
                if evt.Event == "code" {
                    qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
                } else {
                    Log.Infof("QR channel result: %s", evt.Event)
                }
            }
        }()
    }

    cli.AddEventHandler(handler)
    err = cli.Connect()
    if err != nil {
        Log.Errorf("Failed to connect: %v", err)
        return
    }

    c := make(chan os.Signal)
    input := make(chan string)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    go func() {
        defer close(input)
        scan := bufio.NewScanner(os.Stdin)
        for scan.Scan() {
            line := strings.TrimSpace(scan.Text())
            if len(line) > 0 {
                input <- line
            }
        }
    }()
    for {
        select {
        case <-c:
            Log.Infof("Interrupt received, exiting")
            cli.Disconnect()
            return
        case cmd := <-input:
            if len(cmd) == 0 {
                Log.Infof("Stdin closed, exiting")
                cli.Disconnect()
                return
            }
            if isWaitingForPair.Load() {
                if cmd == "r" {
                    pairRejectChan <- true
                } else if cmd == "a" {
                    pairRejectChan <- false
                }
                continue
            }
            args := strings.Fields(cmd)
            cmd = args[0]
            args = args[1:]
            go handleCmd(strings.ToLower(cmd), args)
        }
    }
}

func parseJID(arg string) (types.JID, bool) {
    if arg[0] == '+' {
        arg = arg[1:]
    }
    if !strings.ContainsRune(arg, '@') {
        return types.NewJID(arg, types.DefaultUserServer), true
    } else {
        recipient, err := types.ParseJID(arg)
        if err != nil {
            Log.Errorf("Invalid JID %s: %v", arg, err)
            return recipient, false
        } else if recipient.User == "" {
            Log.Errorf("Invalid JID %s: no server specified", arg)
            return recipient, false
        }
        return recipient, true
    }
}

func handleCmd(cmd string, args []string) {
    switch cmd {
    case "reconnect":
        cli.Disconnect()
        err := cli.Connect()
        if err != nil {
            Log.Errorf("Failed to connect: %v", err)
        }
    case "logout":
        err := cli.Logout()
        if err != nil {
            Log.Errorf("Error Log.ing out: %v", err)
        } else {
            Log.Infof("Successfully Log.ed out")
        }
    case "appstate":
        if len(args) < 1 {
            Log.Errorf("Usage: appstate <types...>")
            return
        }
        names := []appstate.WAPatchName{appstate.WAPatchName(args[0])}
        if args[0] == "all" {
            names = []appstate.WAPatchName{appstate.WAPatchRegular, appstate.WAPatchRegularHigh, appstate.WAPatchRegularLow, appstate.WAPatchCriticalUnblockLow, appstate.WAPatchCriticalBlock}
        }
        resync := len(args) > 1 && args[1] == "resync"
        for _, name := range names {
            err := cli.FetchAppState(name, resync, false)
            if err != nil {
                Log.Errorf("Failed to sync app state: %v", err)
            }
        }
    case "request-appstate-key":
        if len(args) < 1 {
            Log.Errorf("Usage: request-appstate-key <ids...>")
            return
        }
        var keyIDs = make([][]byte, len(args))
        for i, id := range args {
            decoded, err := hex.DecodeString(id)
            if err != nil {
                Log.Errorf("Failed to decode %s as hex: %v", id, err)
                return
            }
            keyIDs[i] = decoded
        }
        cli.DangerousInternals().RequestAppStateKeys(context.Background(), keyIDs)
    case "checkuser":
        if len(args) < 1 {
            Log.Errorf("Usage: checkuser <phone numbers...>")
            return
        }
        resp, err := cli.IsOnWhatsApp(args)
        if err != nil {
            Log.Errorf("Failed to check if users are on WhatsApp:", err)
        } else {
            for _, item := range resp {
                if item.VerifiedName != nil {
                    Log.Infof("%s: on whatsapp: %t, JID: %s, business name: %s", item.Query, item.IsIn, item.JID, item.VerifiedName.Details.GetVerifiedName())
                } else {
                    Log.Infof("%s: on whatsapp: %t, JID: %s", item.Query, item.IsIn, item.JID)
                }
            }
        }
    case "checkupdate":
        resp, err := cli.CheckUpdate()
        if err != nil {
            Log.Errorf("Failed to check for updates: %v", err)
        } else {
            Log.Debugf("Version data: %#v", resp)
            if resp.ParsedVersion == store.GetWAVersion() {
                Log.Infof("Client is up to date")
            } else if store.GetWAVersion().LessThan(resp.ParsedVersion) {
                Log.Warnf("Client is outdated")
            } else {
                Log.Infof("Client is newer than latest")
            }
        }
    case "subscribepresence":
        if len(args) < 1 {
            Log.Errorf("Usage: subscribepresence <jid>")
            return
        }
        jid, ok := parseJID(args[0])
        if !ok {
            return
        }
        err := cli.SubscribePresence(jid)
        if err != nil {
            fmt.Println(err)
        }
    case "presence":
        if len(args) == 0 {
            Log.Errorf("Usage: presence <available/unavailable>")
            return
        }
        fmt.Println(cli.SendPresence(types.Presence(args[0])))
    case "chatpresence":
        if len(args) == 2 {
            args = append(args, "")
        } else if len(args) < 2 {
            Log.Errorf("Usage: chatpresence <jid> <composing/paused> [audio]")
            return
        }
        jid, _ := types.ParseJID(args[0])
        fmt.Println(cli.SendChatPresence(jid, types.ChatPresence(args[1]), types.ChatPresenceMedia(args[2])))
    case "privacysettings":
        resp, err := cli.TryFetchPrivacySettings(false)
        if err != nil {
            fmt.Println(err)
        } else {
            fmt.Printf("%+v\n", resp)
        }
    case "getuser":
        if len(args) < 1 {
            Log.Errorf("Usage: getuser <jids...>")
            return
        }
        var jids []types.JID
        for _, arg := range args {
            jid, ok := parseJID(arg)
            if !ok {
                return
            }
            jids = append(jids, jid)
        }
        resp, err := cli.GetUserInfo(jids)
        if err != nil {
            Log.Errorf("Failed to get user info: %v", err)
        } else {
            for jid, info := range resp {
                Log.Infof("%s: %+v", jid, info)
            }
        }
    case "mediaconn":
        conn, err := cli.DangerousInternals().RefreshMediaConn(false)
        if err != nil {
            Log.Errorf("Failed to get media connection: %v", err)
        } else {
            Log.Infof("Media connection: %+v", conn)
        }
    case "getavatar":
        if len(args) < 1 {
            Log.Errorf("Usage: getavatar <jid> [existing ID] [--preview] [--community]")
            return
        }
        jid, ok := parseJID(args[0])
        if !ok {
            return
        }
        existingID := ""
        if len(args) > 2 {
            existingID = args[2]
        }
        var preview, isCommunity bool
        for _, arg := range args {
            if arg == "--preview" {
                preview = true
            } else if arg == "--community" {
                isCommunity = true
            }
        }
        pic, err := cli.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{
            Preview:     preview,
            IsCommunity: isCommunity,
            ExistingID:  existingID,
        })
        if err != nil {
            Log.Errorf("Failed to get avatar: %v", err)
        } else if pic != nil {
            Log.Infof("Got avatar ID %s: %s", pic.ID, pic.URL)
        } else {
            Log.Infof("No avatar found")
        }
    case "getgroup":
        if len(args) < 1 {
            Log.Errorf("Usage: getgroup <jid>")
            return
        }
        group, ok := parseJID(args[0])
        if !ok {
            return
        } else if group.Server != types.GroupServer {
            Log.Errorf("Input must be a group JID (@%s)", types.GroupServer)
            return
        }
        resp, err := cli.GetGroupInfo(group)
        if err != nil {
            Log.Errorf("Failed to get group info: %v", err)
        } else {
            Log.Infof("Group info: %+v", resp)
        }
    case "subgroups":
        if len(args) < 1 {
            Log.Errorf("Usage: subgroups <jid>")
            return
        }
        group, ok := parseJID(args[0])
        if !ok {
            return
        } else if group.Server != types.GroupServer {
            Log.Errorf("Input must be a group JID (@%s)", types.GroupServer)
            return
        }
        resp, err := cli.GetSubGroups(group)
        if err != nil {
            Log.Errorf("Failed to get subgroups: %v", err)
        } else {
            for _, sub := range resp {
                Log.Infof("Subgroup: %+v", sub)
            }
        }
    case "communityparticipants":
        if len(args) < 1 {
            Log.Errorf("Usage: communityparticipants <jid>")
            return
        }
        group, ok := parseJID(args[0])
        if !ok {
            return
        } else if group.Server != types.GroupServer {
            Log.Errorf("Input must be a group JID (@%s)", types.GroupServer)
            return
        }
        resp, err := cli.GetLinkedGroupsParticipants(group)
        if err != nil {
            Log.Errorf("Failed to get community participants: %v", err)
        } else {
            Log.Infof("Community participants: %+v", resp)
        }
    case "listgroups":
        groups, err := cli.GetJoinedGroups()
        if err != nil {
            Log.Errorf("Failed to get group list: %v", err)
        } else {
            for _, group := range groups {
                Log.Infof("%+v", group)
            }
        }
    case "getinvitelink":
        if len(args) < 1 {
            Log.Errorf("Usage: getinvitelink <jid> [--reset]")
            return
        }
        group, ok := parseJID(args[0])
        if !ok {
            return
        } else if group.Server != types.GroupServer {
            Log.Errorf("Input must be a group JID (@%s)", types.GroupServer)
            return
        }
        resp, err := cli.GetGroupInviteLink(group, len(args) > 1 && args[1] == "--reset")
        if err != nil {
            Log.Errorf("Failed to get group invite link: %v", err)
        } else {
            Log.Infof("Group invite link: %s", resp)
        }
    case "queryinvitelink":
        if len(args) < 1 {
            Log.Errorf("Usage: queryinvitelink <link>")
            return
        }
        resp, err := cli.GetGroupInfoFromLink(args[0])
        if err != nil {
            Log.Errorf("Failed to resolve group invite link: %v", err)
        } else {
            Log.Infof("Group info: %+v", resp)
        }
    case "querybusinesslink":
        if len(args) < 1 {
            Log.Errorf("Usage: querybusinesslink <link>")
            return
        }
        resp, err := cli.ResolveBusinessMessageLink(args[0])
        if err != nil {
            Log.Errorf("Failed to resolve business message link: %v", err)
        } else {
            Log.Infof("Business info: %+v", resp)
        }
    case "joininvitelink":
        if len(args) < 1 {
            Log.Errorf("Usage: acceptinvitelink <link>")
            return
        }
        groupID, err := cli.JoinGroupWithLink(args[0])
        if err != nil {
            Log.Errorf("Failed to join group via invite link: %v", err)
        } else {
            Log.Infof("Joined %s", groupID)
        }
    case "getstatusprivacy":
        resp, err := cli.GetStatusPrivacy()
        fmt.Println(err)
        fmt.Println(resp)
    case "setdisappeartimer":
        if len(args) < 2 {
            Log.Errorf("Usage: setdisappeartimer <jid> <days>")
            return
        }
        days, err := strconv.Atoi(args[1])
        if err != nil {
            Log.Errorf("Invalid duration: %v", err)
            return
        }
        recipient, ok := parseJID(args[0])
        if !ok {
            return
        }
        err = cli.SetDisappearingTimer(recipient, time.Duration(days)*24*time.Hour)
        if err != nil {
            Log.Errorf("Failed to set disappearing timer: %v", err)
        }
    case "send":
        if len(args) < 2 {
            Log.Errorf("Usage: send <jid> <text>")
            return
        }
        recipient, ok := parseJID(args[0])
        if !ok {
            return
        }
        msg := &waProto.Message{Conversation: proto.String(strings.Join(args[1:], " "))}
        resp, err := cli.SendMessage(context.Background(), recipient, msg)
        if err != nil {
            Log.Errorf("Error sending message: %v", err)
        } else {
            Log.Infof("Message sent (server timestamp: %s)", resp.Timestamp)
        }
    case "sendpoll":
        if len(args) < 7 {
            Log.Errorf("Usage: sendpoll <jid> <max answers> <question> -- <option 1> / <option 2> / ...")
            return
        }
        recipient, ok := parseJID(args[0])
        if !ok {
            return
        }
        maxAnswers, err := strconv.Atoi(args[1])
        if err != nil {
            Log.Errorf("Number of max answers must be an integer")
            return
        }
        remainingArgs := strings.Join(args[2:], " ")
        question, optionsStr, _ := strings.Cut(remainingArgs, "--")
        question = strings.TrimSpace(question)
        options := strings.Split(optionsStr, "/")
        for i, opt := range options {
            options[i] = strings.TrimSpace(opt)
        }
        resp, err := cli.SendMessage(context.Background(), recipient, cli.BuildPollCreation(question, options, maxAnswers))
        if err != nil {
            Log.Errorf("Error sending message: %v", err)
        } else {
            Log.Infof("Message sent (server timestamp: %s)", resp.Timestamp)
        }
    case "multisend":
        if len(args) < 3 {
            Log.Errorf("Usage: multisend <jids...> -- <text>")
            return
        }
        var recipients []types.JID
        for len(args) > 0 && args[0] != "--" {
            recipient, ok := parseJID(args[0])
            args = args[1:]
            if !ok {
                return
            }
            recipients = append(recipients, recipient)
        }
        if len(args) == 0 {
            Log.Errorf("Usage: multisend <jids...> -- <text> (the -- is required)")
            return
        }
        msg := &waProto.Message{Conversation: proto.String(strings.Join(args[1:], " "))}
        for _, recipient := range recipients {
            go func(recipient types.JID) {
                resp, err := cli.SendMessage(context.Background(), recipient, msg)
                if err != nil {
                    Log.Errorf("Error sending message to %s: %v", recipient, err)
                } else {
                    Log.Infof("Message sent to %s (server timestamp: %s)", recipient, resp.Timestamp)
                }
            }(recipient)
        }
    case "react":
        if len(args) < 3 {
            Log.Errorf("Usage: react <jid> <message ID> <reaction>")
            return
        }
        recipient, ok := parseJID(args[0])
        if !ok {
            return
        }
        messageID := args[1]
        fromMe := false
        if strings.HasPrefix(messageID, "me:") {
            fromMe = true
            messageID = messageID[len("me:"):]
        }
        reaction := args[2]
        if reaction == "remove" {
            reaction = ""
        }
        msg := &waProto.Message{
            ReactionMessage: &waProto.ReactionMessage{
                Key: &waProto.MessageKey{
                    RemoteJid: proto.String(recipient.String()),
                    FromMe:    proto.Bool(fromMe),
                    Id:        proto.String(messageID),
                },
                Text:              proto.String(reaction),
                SenderTimestampMs: proto.Int64(time.Now().UnixMilli()),
            },
        }
        resp, err := cli.SendMessage(context.Background(), recipient, msg)
        if err != nil {
            Log.Errorf("Error sending reaction: %v", err)
        } else {
            Log.Infof("Reaction sent (server timestamp: %s)", resp.Timestamp)
        }
    case "revoke":
        if len(args) < 2 {
            Log.Errorf("Usage: revoke <jid> <message ID>")
            return
        }
        recipient, ok := parseJID(args[0])
        if !ok {
            return
        }
        messageID := args[1]
        resp, err := cli.SendMessage(context.Background(), recipient, cli.BuildRevoke(recipient, types.EmptyJID, messageID))
        if err != nil {
            Log.Errorf("Error sending revocation: %v", err)
        } else {
            Log.Infof("Revocation sent (server timestamp: %s)", resp.Timestamp)
        }
    case "sendimg":
        if len(args) < 2 {
            Log.Errorf("Usage: sendimg <jid> <image path> [caption]")
            return
        }
        recipient, ok := parseJID(args[0])
        if !ok {
            return
        }
        data, err := os.ReadFile(args[1])
        if err != nil {
            Log.Errorf("Failed to read %s: %v", args[0], err)
            return
        }
        uploaded, err := cli.Upload(context.Background(), data, whatsmeow.MediaImage)
        if err != nil {
            Log.Errorf("Failed to upload file: %v", err)
            return
        }
        msg := &waProto.Message{ImageMessage: &waProto.ImageMessage{
            Caption:       proto.String(strings.Join(args[2:], " ")),
            Url:           proto.String(uploaded.URL),
            DirectPath:    proto.String(uploaded.DirectPath),
            MediaKey:      uploaded.MediaKey,
            Mimetype:      proto.String(http.DetectContentType(data)),
            FileEncSha256: uploaded.FileEncSHA256,
            FileSha256:    uploaded.FileSHA256,
            FileLength:    proto.Uint64(uint64(len(data))),
        }}
        resp, err := cli.SendMessage(context.Background(), recipient, msg)
        if err != nil {
            Log.Errorf("Error sending image message: %v", err)
        } else {
            Log.Infof("Image message sent (server timestamp: %s)", resp.Timestamp)
        }
    case "setstatus":
        if len(args) == 0 {
            Log.Errorf("Usage: setstatus <message>")
            return
        }
        err := cli.SetStatusMessage(strings.Join(args, " "))
        if err != nil {
            Log.Errorf("Error setting status message: %v", err)
        } else {
            Log.Infof("Status updated")
        }
    case "archive":
        if len(args) < 2 {
            Log.Errorf("Usage: archive <jid> <action>")
            return
        }
        target, ok := parseJID(args[0])
        if !ok {
            return
        }
        action, err := strconv.ParseBool(args[1])
        if err != nil {
            Log.Errorf("invalid second argument: %v", err)
            return
        }

        err = cli.SendAppState(appstate.BuildArchive(target, action, time.Time{}, nil))
        if err != nil {
            Log.Errorf("Error changing chat's archive state: %v", err)
        }
    case "mute":
        if len(args) < 2 {
            Log.Errorf("Usage: mute <jid> <action>")
            return
        }
        target, ok := parseJID(args[0])
        if !ok {
            return
        }
        action, err := strconv.ParseBool(args[1])
        if err != nil {
            Log.Errorf("invalid second argument: %v", err)
            return
        }

        err = cli.SendAppState(appstate.BuildMute(target, action, 1*time.Hour))
        if err != nil {
            Log.Errorf("Error changing chat's mute state: %v", err)
        }
    case "pin":
        if len(args) < 2 {
            Log.Errorf("Usage: pin <jid> <action>")
            return
        }
        target, ok := parseJID(args[0])
        if !ok {
            return
        }
        action, err := strconv.ParseBool(args[1])
        if err != nil {
            Log.Errorf("invalid second argument: %v", err)
            return
        }

        err = cli.SendAppState(appstate.BuildPin(target, action))
        if err != nil {
            Log.Errorf("Error changing chat's pin state: %v", err)
        }
    }
}

var historySyncID int32
var startupTime = time.Now().Unix()

func handler(rawEvt interface{}) {
    switch evt := rawEvt.(type) {
    case *events.AppStateSyncComplete:
        if len(cli.Store.PushName) > 0 && evt.Name == appstate.WAPatchCriticalBlock {
            err := cli.SendPresence(types.PresenceAvailable)
            if err != nil {
                Log.Warnf("Failed to send available presence: %v", err)
            } else {
                Log.Infof("Marked self as available")
            }
        }
    case *events.Connected, *events.PushNameSetting:
        if len(cli.Store.PushName) == 0 {
            return
        }
        // Send presence available when connecting and when the pushname is changed.
        // This makes sure that outgoing messages always have the right pushname.
        err := cli.SendPresence(types.PresenceAvailable)
        if err != nil {
            Log.Warnf("Failed to send available presence: %v", err)
        } else {
            Log.Infof("Marked self as available")
        }
    case *events.StreamReplaced:
        os.Exit(0)
    case *events.Message:
        metaParts := []string{fmt.Sprintf("pushname: %s", evt.Info.PushName), fmt.Sprintf("timestamp: %s", evt.Info.Timestamp)}
        if evt.Info.Type != "" {
            metaParts = append(metaParts, fmt.Sprintf("type: %s", evt.Info.Type))
        }
        if evt.Info.Category != "" {
            metaParts = append(metaParts, fmt.Sprintf("category: %s", evt.Info.Category))
        }
        if evt.IsViewOnce {
            metaParts = append(metaParts, "view once")
        }
        if evt.IsViewOnce {
            metaParts = append(metaParts, "ephemeral")
        }
        if evt.IsViewOnceV2 {
            metaParts = append(metaParts, "ephemeral (v2)")
        }
        if evt.IsDocumentWithCaption {
            metaParts = append(metaParts, "document with caption")
        }
        if evt.IsEdit {
            metaParts = append(metaParts, "edit")
        }

        Log.Infof("Received message %s from %s (%s): %+v", evt.Info.ID, evt.Info.SourceString(), strings.Join(metaParts, ", "), evt.Message)
        ReceiveMsg(evt.Message.String())

        if evt.Message.GetPollUpdateMessage() != nil {
            decrypted, err := cli.DecryptPollVote(evt)
            if err != nil {
                Log.Errorf("Failed to decrypt vote: %v", err)
            } else {
                Log.Infof("Selected options in decrypted vote:")
                for _, option := range decrypted.SelectedOptions {
                    Log.Infof("- %X", option)
                }
            }
        } else if evt.Message.GetEncReactionMessage() != nil {
            decrypted, err := cli.DecryptReaction(evt)
            if err != nil {
                Log.Errorf("Failed to decrypt encrypted reaction: %v", err)
            } else {
                Log.Infof("Decrypted reaction: %+v", decrypted)
            }
        }

        img := evt.Message.GetImageMessage()
        if img != nil {
            data, err := cli.Download(img)
            if err != nil {
                Log.Errorf("Failed to download image: %v", err)
                return
            }
            exts, _ := mime.ExtensionsByType(img.GetMimetype())
            path := fmt.Sprintf("%s%s", evt.Info.ID, exts[0])
            err = os.WriteFile(path, data, 0600)
            if err != nil {
                Log.Errorf("Failed to save image: %v", err)
                return
            }
            Log.Infof("Saved image in message to %s", path)
        }
    case *events.Receipt:
        if evt.Type == events.ReceiptTypeRead || evt.Type == events.ReceiptTypeReadSelf {
            Log.Infof("%v was read by %s at %s", evt.MessageIDs, evt.SourceString(), evt.Timestamp)
        } else if evt.Type == events.ReceiptTypeDelivered {
            Log.Infof("%s was delivered to %s at %s", evt.MessageIDs[0], evt.SourceString(), evt.Timestamp)
        }
    case *events.Presence:
        if evt.Unavailable {
            if evt.LastSeen.IsZero() {
                Log.Infof("%s is now offline", evt.From)
            } else {
                Log.Infof("%s is now offline (last seen: %s)", evt.From, evt.LastSeen)
            }
        } else {
            Log.Infof("%s is now online", evt.From)
        }
    case *events.HistorySync:
        id := atomic.AddInt32(&historySyncID, 1)
        fileName := fmt.Sprintf("history-%d-%d.json", startupTime, id)
        file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE, 0600)
        if err != nil {
            Log.Errorf("Failed to open file to write history sync: %v", err)
            return
        }
        enc := json.NewEncoder(file)
        enc.SetIndent("", "  ")
        err = enc.Encode(evt.Data)
        if err != nil {
            Log.Errorf("Failed to write history sync: %v", err)
            return
        }
        Log.Infof("Wrote history sync to %s", fileName)
        _ = file.Close()
    case *events.AppState:
        Log.Debugf("App state event: %+v / %+v", evt.Index, evt.SyncActionValue)
    case *events.KeepAliveTimeout:
        Log.Debugf("Keepalive timeout event: %+v", evt)
        if evt.ErrorCount > 3 {
            Log.Debugf("Got >3 keepalive timeouts, forcing reconnect")
            go func() {
                cli.Disconnect()
                err := cli.Connect()
                if err != nil {
                    Log.Errorf("Error force-reconnecting after keepalive timeouts: %v", err)
                }
            }()
        }
    case *events.KeepAliveRestored:
        Log.Debugf("Keepalive restored")
    }
}
