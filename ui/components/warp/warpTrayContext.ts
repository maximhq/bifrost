import { createContext } from "react";

// False while the composer tray keeps a closed card mounted for its fold-back. That retained element keeps its
// last props, so a card reads this to drop its key handlers and pending answer once its question is gone.
export const WarpTrayActiveContext = createContext(true);