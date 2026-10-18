import { useState } from "react";
import { Composer } from "../Composer";
import { WidgetGrid } from "../widgetgrid/WidgetGrid";
import { Hero } from "./Hero";

/** Home (UI-01): hero with the composer and suggestion chips above the widget grid. */
export function HomePage() {
  const [prompt, setPrompt] = useState("");
  return (
    <div className="space-y-6">
      <h1 className="sr-only">Start</h1>
      <Hero
        onSuggest={(text) => {
          setPrompt(text);
          document.getElementById("prompt")?.focus();
        }}
      >
        <Composer prompt={prompt} onPromptChange={setPrompt} />
      </Hero>
      <WidgetGrid />
    </div>
  );
}
