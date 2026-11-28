import { createRoot } from "react-dom/client";
import App from "./App";
import { applyAppearance, readAppearance } from "./features/settings/appearance";
import "./styles.css";

applyAppearance(readAppearance());
createRoot(document.getElementById("root")!).render(<App />);
