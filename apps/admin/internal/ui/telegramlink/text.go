package telegramlink

import "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"

const (
	defaultTitle        = "Link Telegram"
	safetyNoticeTitle   = "Account safety"
	safetyNotice        = "Use a dedicated Telegram account that is not new, with two-step verification on. Never share your api_id. butterDB keeps a send-rate limit on; if Telegram restricts the account, objects that are not mirrored to a replica account are lost."
	appHelp             = "Get these at my.telegram.org → API development tools"
	appPortalLabel      = "my.telegram.org"
	appPortalURL        = "https://my.telegram.org/apps"
	phoneHelp           = "Enter the phone number of the Telegram account, with its country code."
	waitText            = "Asking Telegram to send the code…"
	passwordHelp        = "This account has two-step verification. Enter its password."
	labelAPIID          = "api_id"
	labelAPIHash        = "api_hash"
	labelPhone          = "Phone number"
	labelCode           = "Login code"
	labelPassword       = "Password"
	labelShow           = "Show"
	buttonSaveApp       = "Save app credentials"
	buttonSendCode      = "Send code"
	buttonSubmitCode    = "Submit code"
	buttonSubmitPass    = "Submit password"
	buttonCancelLogin   = "Cancel login"
	buttonContinue      = "Continue"
	buttonChangeApp     = "Change app credentials"
	changeAppTitle      = "Change app credentials"
	changeAppBody       = "This removes the saved api_id and api_hash from the server so you can enter new ones."
	changeAppConfirm    = "Change"
	codeExpiredText     = "The code has expired."
	placeholderPhone    = "+447700900134"
	placeholderAPIHash  = "32 hexadecimal characters"
	placeholderAPIID    = "digits only"
	codeDeliveryApp     = "Sent to your Telegram app on another device"
	codeDeliverySMS     = "Sent by SMS"
	codeDeliveryCall    = "Telegram will call this number"
	codeDeliveryEmail   = "Sent to your login email"
	codeDeliveryDefault = "Sent by Telegram"
)

func stepTitle(step vm.Step) string {
	switch step {
	case vm.StepApp:
		return "Step 1 of 4 · App credentials"
	case vm.StepPhone:
		return "Step 2 of 4 · Phone number"
	case vm.StepWait:
		return "Sending code"
	case vm.StepCode:
		return "Step 3 of 4 · Login code"
	case vm.StepPassword:
		return "Step 4 of 4 · Two-step password"
	}
	return "Linked"
}

func codeDeliveryText(kind string) string {
	switch kind {
	case "app":
		return codeDeliveryApp
	case "sms", "fragment_sms", "firebase_sms":
		return codeDeliverySMS
	case "call", "flash_call", "missed_call":
		return codeDeliveryCall
	case "email":
		return codeDeliveryEmail
	}
	return codeDeliveryDefault
}
