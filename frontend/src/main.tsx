import { createRoot } from "react-dom/client";
import { installMenus } from "./lib/menus";
import { App } from "./shell/App";

installMenus();
createRoot(document.getElementById("root")!).render(<App />);
