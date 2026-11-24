# App Privacy ("nutrition label") for RemoHop: browser checklist

The App Store Connect API has no endpoint for the App Privacy answers, and fastlane's
`upload_app_privacy_details_to_app_store` (deliver) only works with an Apple ID web session
(username, password, 2FA), not with the CI's API key. So this is set once by hand.

Answers below follow the published privacy policy (site/src/*/privacy.html): the app has no analytics,
advertising, crash-reporting or third-party SDK, and sends nothing to Readori. Confirm them against the
current code before saving, and again if a default rendezvous server operated by Readori is ever built in.

1. https://appstoreconnect.apple.com -> My Apps -> RemoHop -> App Privacy (left sidebar).
2. Privacy Policy URL: the privacy_url.txt value (also set by the `metadata` mode).
3. "Get Started" under Data Types. Question: "Do you or your third-party partners collect data from this app?"
   Answer: **No, we do not collect data from this app.**
   (Data typed in the app goes only to the computer the user connects to, never to Readori.)
4. Save, then **Publish** (top right). Nothing is submitted for review by this.

Check before answering No:
- No Readori-operated rendezvous or relay server is the app's default (policy says there is none).
- Apple's TestFlight/App Store diagnostics are Apple's own and are not "collected by you".

If the answer is ever Yes (for example an account system or a default server that logs IP addresses is built in),
the data types, purposes, linkage and tracking answers must be worked out from what that code really sends; do
not reuse this checklist then.
