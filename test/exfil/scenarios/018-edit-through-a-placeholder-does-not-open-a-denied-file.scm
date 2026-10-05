;; expect: deny
;; ADR 43: fs-edit matches old in the text the model reads, with each
;; credential a placeholder. That is no way into a credential file: the path
;; is refused before the file is read, whatever old says.
(fs-edit ".env" "API_KEY=[REDACTED openai-key]" "API_KEY=x")
