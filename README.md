# Hanzo AI SDK

The Hanzo AI SDK is **a library for building AI-powered streaming text and chat
UIs** powered by the [Hanzo AI](https://hanzo.ai) Platform.

## Features

- [SWR](https://swr.vercel.app)-powered React, Svelte, Vue and Solid helpers for streaming text responses and building chat and completion UIs
- First-class support for [LangChain](https://js.langchain.com/docs) and [OpenAI](https://openai.com), [Anthropic](https://www.anthropic.com), [Cohere](https://cohere.com), [Hugging Face](https://huggingface.co), [Fireworks](https://app.fireworks.ai) and [Replicate](https://replicate.com)
- Node.js, Serverless, and [Edge Runtime](https://edge-runtime.vercel.app/) support
- Callbacks for saving completed streaming responses to a database (in the same request)

## Installation

```sh
pnpm install @hanzo/ai
```

View the full documentation and examples on [sdk.vercel.ai/docs](https://docs.hanzo.ai)

## Example: An AI Chatbot with Next.js and OpenAI

With the Hanzo AI SDK, you can build a ChatGPT-like app in just a few lines of code:

```tsx
// ./app/api/chat/route.js
import OpenAI from 'openai';
import { OpenAIStream, StreamingTextResponse } from 'ai';

const openai = new OpenAI({
  apiKey: process.env.OPENAI_API_KEY,
});

export const runtime = 'edge';

export async function POST(req) {
  const { messages } = await req.json();
  const response = await openai.chat.completions.create({
    model: 'gpt-4',
    stream: true,
    messages,
  });
  const stream = OpenAIStream(response);
  return new StreamingTextResponse(stream);
}
```

```tsx
// ./app/page.js
'use client';

import { useChat } from '@hanzo/ai/react';

export default function Chat() {
  const { messages, input, handleInputChange, handleSubmit } = useChat();

  return (
    <div>
      {messages.map(m => (
        <div key={m.id}>
          {m.role}: {m.content}
        </div>
      ))}

      <form onSubmit={handleSubmit}>
        <input
          value={input}
          placeholder="Say something..."
          onChange={handleInputChange}
        />
      </form>
    </div>
  );
}
```

---

View the full documentation and examples on [docs.hanzo.ai/docs](https://docs.hanzo.ai).
