import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import {applyAppearance, loadAppearance} from "./appearance";
import {LocaleProvider} from "./locales/Locale";
import "./fonts.css";
import "./theme.css";
import "./styles.css";
import "./workspace.css";
import "./features/connection/connection.css";

applyAppearance(loadAppearance());

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <LocaleProvider>
      <App />
    </LocaleProvider>
  </React.StrictMode>,
);
