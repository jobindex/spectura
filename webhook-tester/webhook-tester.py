from flask import Flask, request

app = Flask(__name__)

@app.route('/webhook', methods=["POST"])
def webhook():
    print(request.json)
    return "thanks 👍"
