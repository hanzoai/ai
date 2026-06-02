import {toast} from "sonner";

/**
 * Drop-in replacement for Setting.showMessage() and antd message/notification.
 *
 * Usage:
 *   import { showMessage } from "../lib/toast"
 *   showMessage("success", "Saved")
 *   showMessage("error", "Failed to save")
 */
export function showMessage(type, text) {
  switch (type) {
  case "success":
    toast.success(text);
    break;
  case "error":
    toast.error(text);
    break;
  case "info":
    toast.info(text);
    break;
  case "warning":
    toast.warning(text);
    break;
  default:
    break;
  }
}

export {toast};
