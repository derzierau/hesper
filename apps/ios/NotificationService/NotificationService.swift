import UserNotifications

/// Pushes from the relay carry ids only. This extension will open the
/// end-to-end channel, read the agent and write the real text ("migrations
/// on mini wants to run npm run db:migrate"); until the mobile link library
/// exists it shows the hidden-preview text, never anything from the push.
final class NotificationService: UNNotificationServiceExtension {
    override func didReceive(_ request: UNNotificationRequest, withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void)
    {
        let content = (request.content.mutableCopy() as? UNMutableNotificationContent) ?? UNMutableNotificationContent()
        content.title = "Hesper"
        content.body = "Agent needs you"
        contentHandler(content)
    }
}
