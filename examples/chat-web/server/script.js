// AI Chat Assistant - Main script file
class AIChatAssistant {
    constructor() {
        // DOM element references
    this.elements = {
            chatMessages: document.getElementById('chatMessages'),
            messageInput: document.getElementById('messageInput'),
            sendButton: document.getElementById('sendButton'),
            settingsToggle: document.getElementById('settingsToggle'),
            settingsPanel: document.getElementById('settingsPanel'),
            closeSettings: document.getElementById('closeSettings'),
            apiUrl: document.getElementById('apiUrl'),
            currentApiUrl: document.getElementById('currentApiUrl'),
            useStream: document.getElementById('useStream'),
            clearChat: document.getElementById('clearChat')
        };
        
        // Application settings
    this.settings = {
        baseUrl: this.elements.apiUrl.value,
        useStream: this.elements.useStream.checked
    };
    
    // Initialize application
    this.init();
    }
    
    // Initialize application
    init() {
        this.loadSettings();
        this.bindEvents();
        this.setupMarked();
    }
    
    // Setup marked.js options
    setupMarked() {
        marked.setOptions({
            breaks: true,       // Enable GFM line breaks (\n converts to <br>)
            gfm: true,          // Enable GitHub-style Markdown
            headerIds: false,   // Disable header ID generation
            sanitize: false,    // Disable HTML sanitization
            highlight: function(code, lang) {
                // Code highlighting support (can integrate highlight.js library if needed)
                return code;
            }
        });
    }
    
    // Bind event listeners
    bindEvents() {
        // API URL change listener
        this.elements.apiUrl.addEventListener('input', (e) => {
            this.settings.baseUrl = e.target.value;
            this.elements.currentApiUrl.textContent = e.target.value;
            this.saveSetting('apiUrl', e.target.value);
        });
        
        // Streaming response toggle listener
        this.elements.useStream.addEventListener('change', (e) => {
            this.settings.useStream = e.target.checked;
            this.saveSetting('useStream', e.target.checked);
        });
        
        // Send message button
        this.elements.sendButton.addEventListener('click', () => this.sendMessage());
        
        // Input field events
        this.elements.messageInput.addEventListener('keypress', (e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                this.sendMessage();
            }
        });
        
        // Input field height adjustment
        this.elements.messageInput.addEventListener('input', () => {
            this.elements.messageInput.style.height = 'auto';
            this.elements.messageInput.style.height = Math.min(this.elements.messageInput.scrollHeight, 120) + 'px';
        });
        
        // Settings panel interaction
        this.bindSettingsEvents();
    }
    
    // Bind settings panel events
    bindSettingsEvents() {
        // Toggle settings panel display
        this.elements.settingsToggle.addEventListener('click', () => {
            this.elements.settingsPanel.classList.toggle('open');
        });
        
        // Close settings panel
        this.elements.closeSettings.addEventListener('click', () => {
            this.elements.settingsPanel.classList.remove('open');
        });
        
        // Close when clicking outside settings panel
        document.addEventListener('click', (e) => {
            if (!this.elements.settingsPanel.contains(e.target) && e.target !== this.elements.settingsToggle) {
                this.elements.settingsPanel.classList.remove('open');
            }
        });
        
        // Clear chat history
        this.elements.clearChat.addEventListener('click', () => {
            if (confirm('Are you sure you want to clear chat history?')) {
                this.clearChatHistory();
            }
        });
    }
    
    // Load settings from local storage
    loadSettings() {
        const savedUrl = this.getSetting('apiUrl');
        if (savedUrl) {
            this.settings.baseUrl = savedUrl;
            this.elements.apiUrl.value = savedUrl;
            this.elements.currentApiUrl.textContent = savedUrl;
        }
        
        const savedUseStream = this.getSetting('useStream');
        if (savedUseStream !== null) {
            this.settings.useStream = savedUseStream;
            this.elements.useStream.checked = savedUseStream;
        }
    }
    
    // Save setting to local storage
    saveSetting(key, value) {
        localStorage.setItem(`chat_${key}`, JSON.stringify(value));
    }
    
    // Get setting from local storage
    getSetting(key) {
        const value = localStorage.getItem(`chat_${key}`);
        if (value !== null) {
            try {
                return JSON.parse(value);
            } catch {
                return value;
            }
        }
        return null;
    }
    
    // Send message
    sendMessage() {
        const message = this.elements.messageInput.value.trim();
        if (!message) return;
        
        // Add user message to chat (user messages also get Markdown rendering)
        this.addMessage(message, 'user');
        
        // Clear input field
        this.elements.messageInput.value = '';
        this.elements.messageInput.style.height = 'auto';
        
        // Disable send button
        this.elements.sendButton.disabled = true;
        
        // Call API
        if (this.settings.useStream) {
            this.callStreamAPI(message);
        } else {
            this.callAPI(message);
        }
    }
    
    // Regular API call
    async callAPI(message) {
        try {
            const response = await fetch(`${this.settings.baseUrl}/chat`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ message }),
            });
            
            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }
            
            const data = await response.json();
            
            // Directly use response content, don't process tool calls
            let content = data.content;
            
            // Check if content is JSON, don't display if it is
            if (!this.isPureJSON(content)) {
                this.addMessage(content, 'ai');
            }
        } catch (error) {
            console.error('Error calling API:', error);
            this.addMessage(`Sorry, an error occurred when calling the API: ${error.message}`, 'ai');
        } finally {
            this.elements.sendButton.disabled = false;
        }
    }
    
    // Streaming API call
    callStreamAPI(message) {
        // Add AI reply placeholder
        const messageId = `ai-message-${Date.now()}`;
        this.addMessage('', 'ai', messageId, true);
        
        // Flag to track if non-JSON content has been added
        let hasNonJSONContent = false;
        
        try {
            // Create EventSource
            const eventSource = new EventSource(`${this.settings.baseUrl}/chat/stream?message=${encodeURIComponent(message)}`);
            
            // Process message chunks
            eventSource.addEventListener('message', (event) => {
                try {
                    // First try to parse event data directly
                    console.log('Received SSE event:', event.data);
                    
                    try {
                        const data = JSON.parse(event.data);
                        
                        if (data.type === 'chunk') {
                            // Check if content is JSON, only update if it's not
                            if (!this.isPureJSON(data.content)) {
                                hasNonJSONContent = true;
                                this.updateMessage(messageId, data.content);
                            }
                        } else if (data.type === 'error') {
                            hasNonJSONContent = true;
                            this.updateMessage(messageId, `Error: ${data.error}`);
                            eventSource.close();
                        } else if (data.type === 'end') {
                            // Streaming ended, remove entire message if no non-JSON content
                            if (!hasNonJSONContent) {
                                const messageDiv = document.getElementById(messageId);
                                if (messageDiv) {
                                    messageDiv.remove();
                                }
                            } else {
                                this.removeTypingIndicator(messageId);
                            }
                            eventSource.close();
                        } else {
                            // Unknown type, treat as plain text
                            if (!this.isPureJSON(event.data)) {
                                hasNonJSONContent = true;
                                this.updateMessage(messageId, event.data);
                            }
                        }
                    } catch (parseError) {
                        console.log('Event data is not JSON, treating as text:', event.data);
                        // If parsing fails, try to treat event data as text directly
                            hasNonJSONContent = true;
                            this.updateMessage(messageId, event.data);
                    }
                } catch (error) {
                    console.error('Error processing SSE event:', error);
                    // Ensure content is displayed even if processing fails
                    hasNonJSONContent = true;
                    this.updateMessage(messageId, event.data);
                }
            });
            
            // Handle errors
            eventSource.addEventListener('error', (error) => {
                console.error('SSE error:', error);
                this.updateMessage(messageId, 'Sorry, an error occurred during streaming.');
                this.removeTypingIndicator(messageId);
                eventSource.close();
            });
            
            // Handle connection close
            eventSource.addEventListener('close', () => {
                this.elements.sendButton.disabled = false;
            });
            
        } catch (error) {
            console.error('Error setting up SSE:', error);
            this.updateMessage(messageId, `Sorry, an error occurred while establishing connection: ${error.message}`);
            this.removeTypingIndicator(messageId);
            this.elements.sendButton.disabled = false;
        }
    }
    
    // Add message to chat
    addMessage(content, sender, id = null, showTyping = false) {
        const messageDiv = document.createElement('div');
        messageDiv.className = `message ${sender}-message`;
        
        if (id) {
            messageDiv.id = id;
        }
        
        const headerIcon = sender === 'user' ? 'fa-user' : 'fa-robot';
        const headerText = sender === 'user' ? 'You' : 'AI Assistant';
        const headerColor = sender === 'user' ? '' : 'text-indigo-500';
        
        // Render content
        const contentHtml = this.renderContent(content);
        
        messageDiv.innerHTML = `
            <div class="message-header">
                <i class="fa ${headerIcon} ${headerColor}"></i>
                <span>${headerText}</span>
            </div>
            <div class="message-content">${contentHtml}</div>
            ${showTyping ? '<div class="typing-indicator"><div class="typing-dot"></div><div class="typing-dot"></div><div class="typing-dot"></div></div>' : ''}
        `;
        
        this.elements.chatMessages.appendChild(messageDiv);
        this.scrollToBottom();
        
        return messageDiv;
    }
    
    // Update message content
    updateMessage(messageId, content) {
        const messageDiv = document.getElementById(messageId);
        if (!messageDiv) return;
        
        const contentDiv = messageDiv.querySelector('.message-content');
        
        // Plain text content - use marked.js library for Markdown rendering
        // Fix streaming rendering issue: ensure content is correctly accumulated
        if (content && content.length > 0) {
            if (contentDiv.dataset.accumulatedContent === undefined) {
                // Initialize accumulated content
                contentDiv.dataset.accumulatedContent = content;
            } else {
                // Accumulate content
                contentDiv.dataset.accumulatedContent += content;
            }
            
            try {
                // Use accumulated complete content for Markdown rendering
                contentDiv.innerHTML = marked.parse(contentDiv.dataset.accumulatedContent);
            } catch (renderError) {
                console.error('Error rendering Markdown:', renderError);
                // When rendering fails, display original content
                contentDiv.textContent = contentDiv.dataset.accumulatedContent;
            }
            
            this.scrollToBottom();
        }
    }
    
    // Check if content is pure JSON
    isPureJSON(content) {
        if (!content || typeof content !== 'string') {
            return false;
        }
        
        // Remove leading/trailing whitespace
        const trimmedContent = content.trim();
        
        // Check if content starts with { or [ and ends with } or ]
        if (!((trimmedContent.startsWith('{') && trimmedContent.endsWith('}')) || 
              (trimmedContent.startsWith('[') && trimmedContent.endsWith(']')))) {
            return false;
        }
        
        // Try parsing as JSON
        try {
            JSON.parse(trimmedContent);
            return true;
        } catch (e) {
            return false;
        }
    }
    
    // Render content
    renderContent(content) {
        if (!content) return '';
        
        // Plain text content - use marked.js library for Markdown rendering
        try {
            return marked.parse(content);
        } catch (renderError) {
            console.error('Error rendering Markdown:', renderError);
            // When rendering fails, return escaped original content
            const div = document.createElement('div');
            div.textContent = content;
            return div.innerHTML;
        }
    }
    
    // Remove typing indicator
    removeTypingIndicator(messageId) {
        const messageDiv = document.getElementById(messageId);
        if (messageDiv) {
            const typingIndicator = messageDiv.querySelector('.typing-indicator');
            if (typingIndicator) {
                typingIndicator.remove();
            }
        }
    }
    
    // Scroll to bottom
    scrollToBottom() {
        this.elements.chatMessages.scrollTop = this.elements.chatMessages.scrollHeight;
    }
    
    // Copy to clipboard
    copyToClipboard(text) {
        navigator.clipboard.writeText(text).then(() => {
            // Show copy success notification
            this.showCopyToast();
        }).catch(err => {
            console.error('Copy failed:', err);
        });
    }
    
    // Show copy success notification
    showCopyToast() {
        const notification = document.createElement('div');
        notification.className = 'copy-toast';
        notification.innerHTML = '<i class="fa fa-check"></i> Copied successfully';
        document.body.appendChild(notification);

        // Add animation effect
        setTimeout(() => {
            notification.classList.add('show');
        }, 10);

        // Remove notification after 2 seconds
        setTimeout(() => {
            notification.classList.remove('show');
            setTimeout(() => {
                document.body.removeChild(notification);
            }, 300);
        }, 2000);
    }
    
    // Clear chat history
    clearChatHistory() {
        // Remove all messages except the initial welcome message
        const messages = this.elements.chatMessages.querySelectorAll('.message');
        messages.forEach((message, index) => {
            if (index > 0) {
                message.remove();
            }
        });
    }
}

// Initialize application after page loads
let aiChat;
document.addEventListener('DOMContentLoaded', () => {
    aiChat = new AIChatAssistant();
});

// Global function for collapsing/expanding tool call results
function toggleToolCall(callId) {
    if (aiChat) {
        aiChat.toggleToolCall(callId);
    }
}