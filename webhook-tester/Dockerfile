FROM python:3.11

RUN pip install flask

WORKDIR /app
COPY webhook-tester.py .

CMD ["flask", "--app", "webhook-tester", "run", "--host", "0.0.0.0"]
